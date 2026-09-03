//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// storeMultiSection seeds a two-section document and returns its id + section ids.
func storeMultiSection(t *testing.T, f *authzFixture, ctx context.Context) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	slug := "d" + uuid.NewString()
	md := "# Title\n\n## First\nbody one\n\n## Second\nbody two"
	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug, md, true, "seed", nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Document.Sections, 2)
	ids := make([]uuid.UUID, len(res.Document.Sections))
	for i, s := range res.Document.Sections {
		ids[i] = s.ID
	}
	return res.Document.ID, ids
}

func countRows(t *testing.T, f *authzFixture, model any, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(model).Where("id = ?", id).Count(&n).Error)
	return n
}

func countDeletionEvents(t *testing.T, f *authzFixture) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.DeletionEvent{}).Count(&n).Error)
	return n
}

// TestDeleteSectionMidDocument: an editor deletes one section of a multi-section
// document; the document and its other section remain intact, no deletion_event.
func TestDeleteSectionMidDocument(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	docID, secIDs := storeMultiSection(t, f, ctx)
	events := countDeletionEvents(t, f)

	require.NoError(t, f.svc.DeleteSection(ctx, secIDs[0], nil), "editor deletes mid-doc section")

	require.Equal(t, int64(0), countRows(t, f, &models.Section{}, secIDs[0]), "deleted section is gone")
	require.Equal(t, int64(1), countRows(t, f, &models.Section{}, secIDs[1]), "sibling section remains")
	require.Equal(t, int64(1), countRows(t, f, &models.Document{}, docID), "document remains")
	require.Equal(t, events, countDeletionEvents(t, f), "no deletion_event written")
}

// TestDeleteSectionLastRemovesDocument: deleting a document's only section also
// deletes the now-empty parent document, and still writes no deletion_event.
func TestDeleteSectionLastRemovesDocument(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	events := countDeletionEvents(t, f)

	require.NoError(t, f.svc.DeleteSection(ctx, f.secA, nil), "editor deletes last section")

	require.Equal(t, int64(0), countRows(t, f, &models.Section{}, f.secA), "section is gone")
	require.Equal(t, int64(0), countRows(t, f, &models.Document{}, f.docA), "empty document is gone")
	require.Equal(t, events, countDeletionEvents(t, f), "no deletion_event written")
}

// TestDeleteSectionNonEditorRefused: a non-admin cannot delete an in-scope
// common-pool section (same ErrInvalidInput as update_section); it is unchanged.
func TestDeleteSectionNonEditorRefused(t *testing.T) {
	f := newAuthzFixture(t)
	user := ctxFor(f.tenantA, f.subjA)

	err := f.svc.DeleteSection(user, f.secC, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "non-editor common-pool delete refused")
	require.Equal(t, int64(1), countRows(t, f, &models.Section{}, f.secC), "section unchanged")
}

// TestDeleteSectionUnknownID: a wholly-unknown id returns ErrNotFound.
func TestDeleteSectionUnknownID(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	err := f.svc.DeleteSection(ctx, uuid.New(), nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "unknown id is not found")
}

// TestDeleteSectionCrossTenantOpaque: an id in a tenant the caller cannot read is
// ErrNotFound (identical to unknown), never revealing existence; it is unchanged.
func TestDeleteSectionCrossTenantOpaque(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	err := f.svc.DeleteSection(ctx, f.secB, nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "cross-tenant id is opaque")
	require.Equal(t, int64(1), countRows(t, f, &models.Section{}, f.secB), "foreign section unchanged")
}

// TestDeleteSectionAdminOverride: an admin deletes a common-pool section via the
// tenant override; the section is removed and no deletion_event is written.
func TestDeleteSectionAdminOverride(t *testing.T) {
	f := newAuthzFixture(t)
	admin := ctxFor(f.tenantA, f.admin)
	events := countDeletionEvents(t, f)

	require.NoError(t, f.svc.DeleteSection(admin, f.secC, &models.BootstrapTenantID), "admin override delete")

	require.Equal(t, int64(0), countRows(t, f, &models.Section{}, f.secC), "common-pool section gone")
	require.Equal(t, events, countDeletionEvents(t, f), "no deletion_event written")
}
