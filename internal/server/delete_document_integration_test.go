//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

// createDoc POSTs a document via POST /documents and returns its id (201
// required). tenantID nil ⇒ target the caller's context tenant; non-nil ⇒ send
// an explicit tenant_id (admin/manager cross-tenant create).
func createDoc(t *testing.T, f *writeAPIFixture, ctx context.Context, tenantID *uuid.UUID, category, slug, content string) uuid.UUID {
	t.Helper()
	payload := map[string]any{"category": category, "slug": slug, "content": content}
	if tenantID != nil {
		payload["tenant_id"] = tenantID.String()
	}
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPost, "/documents", ctx, payload))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var doc models.Document
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.NotEqual(t, uuid.Nil, doc.ID)
	return doc.ID
}

// TestDeleteDocumentByID_HomePathCollisionDoesNotDeleteWrongDoc is the B2
// regression. A caller whose home tenant holds a document at path P, and who can
// READ a common-pool document at the SAME path P, issues DELETE by the
// common-pool document's id. The fixed handler deletes by id against the doc's
// OWNING tenant: the common-pool doc requires document#editor (which the caller
// lacks) so the delete is refused (400) and NOTHING is deleted. The pre-fix code
// re-resolved path P against the caller's home tenant and silently deleted the
// caller's OWN home-tenant doc (204).
func TestDeleteDocumentByID_HomePathCollisionDoesNotDeleteWrongDoc(t *testing.T) {
	f := newWriteAPIFixture(t)

	tenantA, err := f.svc.CreateTenant(f.adminCtx, "del-a-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenantA.ID, subj)))

	// Same path P in both tenants.
	category := "learnings"
	slug := "collide-" + uuid.NewString()

	// Y: the caller's OWN home-tenant doc at path P (created by the manager).
	yID := createDoc(t, f, userCtx(tenantA.ID, subj), nil, category, slug,
		"# Home Doc\n\n## Body\nhome tenant content for "+slug)

	// X: a common-pool doc at the SAME path P (created by a system admin).
	common := models.BootstrapTenantID
	xID := createDoc(t, f, adminReqCtx(common), &common, category, slug,
		"# Common Doc\n\n## Body\ncommon pool content for "+slug)
	require.NotEqual(t, yID, xID)

	// Caller (home = A, no editor on the common pool) deletes by the common doc's id.
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodDelete, "/documents/"+xID.String(), userCtx(tenantA.ID, subj), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// The caller's own home-tenant doc Y must be UNTOUCHED (the pre-fix bug deleted it).
	var yCount int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", yID).Count(&yCount).Error)
	require.Equal(t, int64(1), yCount, "home-tenant doc must not be deleted by a by-id delete of a same-path foreign doc")

	// The requested common-pool doc X must also still exist (delete was refused).
	var xCount int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", xID).Count(&xCount).Error)
	require.Equal(t, int64(1), xCount, "common-pool doc must not be deleted by a non-editor")
}

// TestDeleteDocumentByID_DeletesOwnDocument proves the happy path still works:
// a caller deletes their own home-tenant document by id (204) and both the doc
// and its sections are gone.
func TestDeleteDocumentByID_DeletesOwnDocument(t *testing.T) {
	f := newWriteAPIFixture(t)

	tenant, err := f.svc.CreateTenant(f.adminCtx, "del-own-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, subj)))

	slug := "own-" + uuid.NewString()
	id := createDoc(t, f, userCtx(tenant.ID, subj), nil, "learnings", slug,
		"# Own Doc\n\n## Body\ndeletable content for "+slug)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodDelete, "/documents/"+id.String(), userCtx(tenant.ID, subj), nil))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	var docCount, secCount int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", id).Count(&docCount).Error)
	require.Equal(t, int64(0), docCount, "own document must be deleted")
	require.NoError(t, f.db.Table("sections").Where("document_id = ?", id).Count(&secCount).Error)
	require.Equal(t, int64(0), secCount, "sections of the deleted document must be removed")
}
