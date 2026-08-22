//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// newImportPGHandler builds a real DB-backed apiHandler for the relational
// import surface. Unlike the nil-DB authz-only unit tests, the import pre-check
// (hasAnyImportTarget -> WritableTenants) resolves a manager's managed tenants
// against the DB, so a manager-success test needs Postgres. Reuses openAPIPG /
// apiTestDim from api_integration_test.go (same package + build tag).
func newImportPGHandler(t *testing.T) (*apiHandler, authz.Store, *gorm.DB) {
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
		store,
	)
	h := &apiHandler{memory: svc, importJobs: &fakeImportJobs{}, maxUploadBytes: 1 << 20}
	return h, store, db
}

// TestRelationalImportEnqueue_ManagerOfTargetPG proves a tenant#manager (not a
// system admin) passes the identity-level pre-check and enqueues into the tenant
// they manage (202). The pre-check resolves the caller's managed tenants against
// the DB, so this is the DB-backed successor to the former nil-DB manager unit
// test in import_authz_test.go.
func TestRelationalImportEnqueue_ManagerOfTargetPG(t *testing.T) {
	h, store, db := newImportPGHandler(t)
	ctx := context.Background()

	target := models.Tenant{ID: uuid.New(), Name: "imp-mgr-" + uuid.NewString()}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	subj := "mgr-" + uuid.NewString()
	if err := store.Write(ctx, authzseed.TenantManager(target.ID, subj)); err != nil {
		t.Fatalf("seed manager tuple: %v", err)
	}

	body, ct := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload"), target.ID.String())
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithSubject(req.Context(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	fake := h.importJobs.(*fakeImportJobs)
	if fake.created == nil || fake.created.TenantID != target.ID {
		t.Fatalf("expected a job created for tenant %s, got %+v", target.ID, fake.created)
	}
}
