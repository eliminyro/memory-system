//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// writeAPIFixture is a real apiHandler over a real MemoryService plus the raw
// db/store/admin-ctx, so the write-surface tests can seed tenants, grants, and
// per-tenant settings and drive the handlers end to end.
type writeAPIFixture struct {
	h        *apiHandler
	svc      *service.MemoryService
	store    authz.Store
	db       *gorm.DB
	adminCtx context.Context
}

func newWriteAPIFixture(t *testing.T) *writeAPIFixture {
	t.Helper()
	db := openAPIPG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(apiTestDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewThresholdStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		repository.NewRecallReceiptRepository(db),
		store,
	)
	return &writeAPIFixture{
		h:        &apiHandler{memory: svc},
		svc:      svc,
		store:    store,
		db:       db,
		adminCtx: auth.WithLocalAdmin(context.Background()),
	}
}

// userCtx is the request context a bearer + UserContextBridge would set for a
// non-admin caller: the home tenant plus a user subject the authz store resolves.
func userCtx(tid uuid.UUID, subj string) context.Context {
	return auth.WithSubject(auth.WithTenantID(context.Background(), tid), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
}

// adminReqCtx is the request context for a system-admin caller scoped to a tenant.
func adminReqCtx(tid uuid.UUID) context.Context {
	return auth.WithLocalAdmin(auth.WithTenantID(context.Background(), tid))
}

// ctxJSONReq builds a JSON request carrying ctx; a nil payload sends no body (GET).
func ctxJSONReq(method, target string, ctx context.Context, payload any) *http.Request {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, body)
	return req.WithContext(ctx)
}

// TestCreateDocument_ManagerCreatesIntoManagedTenant proves a tenant#manager may
// POST /documents into the tenant they manage: 201, and the doc is persisted
// under that tenant.
func TestCreateDocument_ManagerCreatesIntoManagedTenant(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "wr-mgr-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, subj)))

	slug := "note-" + uuid.NewString()
	payload := map[string]any{
		"category": "learnings",
		"slug":     slug,
		"content":  "# Title " + slug + "\n\n## Heading\ndistinctive body text",
	}
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPost, "/documents", userCtx(tenant.ID, subj), payload))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc models.Document
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.NotEqual(t, uuid.Nil, doc.ID)
	require.Equal(t, tenant.ID, doc.TenantID)
	require.Equal(t, slug, doc.Slug)

	var count int64
	require.NoError(t, f.db.Model(&models.Document{}).
		Where("tenant_id = ? AND slug = ?", tenant.ID, slug).Count(&count).Error)
	require.Equal(t, int64(1), count, "document must be persisted under the managed tenant")
}

// TestCreateDocument_NoManageRightsRefused proves a caller with no manage rights
// on the target tenant is refused at the handler (403), no document written.
func TestCreateDocument_NoManageRightsRefused(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "wr-none-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	stranger := "stranger-" + uuid.NewString() // no tuple on this tenant
	slug := "note-" + uuid.NewString()
	payload := map[string]any{
		"category": "learnings",
		"slug":     slug,
		"content":  "# t\n\n## h\nbody",
	}
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPost, "/documents", userCtx(tenant.ID, stranger), payload))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	var count int64
	require.NoError(t, f.db.Model(&models.Document{}).
		Where("tenant_id = ? AND slug = ?", tenant.ID, slug).Count(&count).Error)
	require.Equal(t, int64(0), count, "a refused create must not persist a document")
}

// TestCreateDocument_DuplicateGuardConflict proves that with the tenant's
// duplicate guard enabled, POSTing a duplicate (force=false) returns 409 with the
// similar_exists status instead of writing a second document.
func TestCreateDocument_DuplicateGuardConflict(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "wr-dup-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	dg := true
	_, err = f.svc.UpdateTenant(f.adminCtx, tenant.ID, service.UpdateTenantFields{DuplicateGuard: &dg})
	require.NoError(t, err)

	// Byte-identical content ⇒ identical FakeEmbedder vectors ⇒ cosine 1.0, above
	// the 0.70 duplicate-guard threshold. Different slug so the guard's own-path
	// exclusion does not skip the collider.
	content := "# Shared Title\n\n## Body\nidentical distinctive content for the duplicate guard"

	rec1 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec1, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "learnings", "slug": "dup-a-" + uuid.NewString(), "content": content,
	}))
	require.Equal(t, http.StatusCreated, rec1.Code, rec1.Body.String())

	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "learnings", "slug": "dup-b-" + uuid.NewString(), "content": content,
	}))
	require.Equal(t, http.StatusConflict, rec2.Code, rec2.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &body))
	require.Equal(t, "similar_exists", body["status"])
}

// TestCreateDocument_EpisodicBypassesDuplicateGuard proves a journal-category
// (episodic) document is never refused as similar_exists, even with the
// tenant's duplicate guard on and byte-identical content — unlike the
// non-episodic case in TestCreateDocument_DuplicateGuardConflict above.
func TestCreateDocument_EpisodicBypassesDuplicateGuard(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "wr-journal-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	dg := true
	_, err = f.svc.UpdateTenant(f.adminCtx, tenant.ID, service.UpdateTenantFields{DuplicateGuard: &dg})
	require.NoError(t, err)

	content := "# Shared Title\n\n## Body\nidentical distinctive content for the journal guard"

	rec1 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec1, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "journal", "slug": "day-a-" + uuid.NewString(), "content": content,
	}))
	require.Equal(t, http.StatusCreated, rec1.Code, rec1.Body.String())

	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "journal", "slug": "day-b-" + uuid.NewString(), "content": content,
	}))
	require.Equal(t, http.StatusCreated, rec2.Code, rec2.Body.String())

	var doc models.Document
	require.NoError(t, f.db.Where("tenant_id = ? AND slug LIKE ?", tenant.ID, "day-b-%").First(&doc).Error)
	require.Equal(t, models.DocTypeJournal, doc.DocType)
}

// TestCreateDocument_ValidationErrors proves the input guards: a bad body is 400,
// and missing required fields are 400 before any authz/store work.
func TestCreateDocument_ValidationErrors(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "wr-val-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/documents", bytes.NewReader([]byte("{not json")))
	f.h.mux().ServeHTTP(rec, req.WithContext(adminReqCtx(tenant.ID)))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "learnings", "slug": "", "content": "x",
	}))
	require.Equal(t, http.StatusBadRequest, rec2.Code, rec2.Body.String())

	// B8: a non-empty but malformed slug is rejected by the shared path validator
	// (the HTTP surface previously skipped this, diverging from MCP store_memory).
	recBad := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recBad, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": "learnings", "slug": "has spaces", "content": "# t\n\n## h\nbody",
	}))
	require.Equal(t, http.StatusBadRequest, recBad.Code, recBad.Body.String())

	// B8: an over-long category (> the varchar(50) column) is a clean 400 from the
	// validator, not a Postgres "value too long" 500 at write time.
	recLong := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recLong, ctxJSONReq(http.MethodPost, "/documents", adminReqCtx(tenant.ID), map[string]any{
		"category": strings.Repeat("a", 51), "slug": "ok", "content": "# t\n\n## h\nbody",
	}))
	require.Equal(t, http.StatusBadRequest, recLong.Code, recLong.Body.String())
}
