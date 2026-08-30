//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

func firstSectionID(t *testing.T, f *writeAPIFixture, docID uuid.UUID) uuid.UUID {
	t.Helper()
	var sec models.Section
	require.NoError(t, f.db.Where("document_id = ?", docID).First(&sec).Error)
	return sec.ID
}

// TestDeleteSection_DeletesOwnSection: DELETE /sections/{id} on a caller's own
// single-section doc returns 204 and removes both the section and the now-empty
// parent document (last-section behavior).
func TestDeleteSection_DeletesOwnSection(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "delsec-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, subj)))

	slug := "sec-" + uuid.NewString()
	docID := createDoc(t, f, userCtx(tenant.ID, subj), nil, "learnings", slug,
		"# Doc\n\n## Body\ndeletable section content for "+slug)
	secID := firstSectionID(t, f, docID)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodDelete, "/sections/"+secID.String(), userCtx(tenant.ID, subj), nil))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	var secCount, docCount int64
	require.NoError(t, f.db.Model(&models.Section{}).Where("id = ?", secID).Count(&secCount).Error)
	require.Equal(t, int64(0), secCount, "section must be deleted")
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", docID).Count(&docCount).Error)
	require.Equal(t, int64(0), docCount, "last-section delete removes the empty document")
}

// TestDeleteSection_UnknownIDNotFound: DELETE of an unknown section id is 404.
func TestDeleteSection_UnknownIDNotFound(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "delsec404-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, subj)))

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodDelete, "/sections/"+uuid.NewString(), userCtx(tenant.ID, subj), nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
