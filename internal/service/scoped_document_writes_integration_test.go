//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// loadDoc reads a document row directly, so a test can assert on columns the
// service never returns.
func loadDoc(t *testing.T, f *authzFixture, id uuid.UUID) models.Document {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, id).Error)
	return d
}

// The regression: put_section persisted the document with gorm.Save, which
// writes every column, so it reverted a title set by anyone else.
func TestPutSection_DoesNotRevertTitle(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	_, err := f.svc.UpdateDocumentTitle(ctx, doc, "A Deliberate Title", nil)
	require.NoError(t, err)

	_, err = f.svc.PutSection(ctx, cat, nil, slug, "Another Heading", "another body", nil)
	require.NoError(t, err)

	require.Equal(t, "A Deliberate Title", loadDoc(t, f, doc).Title,
		"a section write must not write back columns it was not given")
}

// The same defect from the other side: the title write must touch the title.
func TestUpdateDocumentTitle_LeavesOtherColumnsAlone(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)

	before := loadDoc(t, f, doc)

	_, err := f.svc.UpdateDocumentTitle(ctx, doc, "Renamed", nil)
	require.NoError(t, err)

	after := loadDoc(t, f, doc)
	require.Equal(t, "Renamed", after.Title)
	require.Equal(t, before.DocType, after.DocType)
	require.Equal(t, before.Category, after.Category)
	require.Equal(t, before.Slug, after.Slug)
	require.Equal(t, before.Pinned, after.Pinned)
	require.Equal(t, before.CreatedAt.UnixMicro(), after.CreatedAt.UnixMicro())
}

// Narrowing the write must not lose the timestamps the save used to move.
func TestPutSection_StillAdvancesBothClocks(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	before := loadDoc(t, f, doc)
	time.Sleep(10 * time.Millisecond)

	_, err := f.svc.PutSection(ctx, cat, nil, slug, "Second", "second body", nil)
	require.NoError(t, err)

	after := loadDoc(t, f, doc)
	require.True(t, after.UpdatedAt.After(before.UpdatedAt), "the document changed")
	require.NotNil(t, after.LastAccessedAt, "a write keeps the document warm against eviction")
	if before.LastAccessedAt != nil {
		require.False(t, after.LastAccessedAt.Before(*before.LastAccessedAt),
			"the access clock must not go backwards")
	}
}

func TestUpdateDocumentTitle_AdvancesUpdatedAt(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)

	before := loadDoc(t, f, doc).UpdatedAt
	time.Sleep(10 * time.Millisecond)

	_, err := f.svc.UpdateDocumentTitle(ctx, doc, "Renamed Again", nil)
	require.NoError(t, err)

	require.True(t, loadDoc(t, f, doc).UpdatedAt.After(before))
}

// The whole-document path is deliberately untouched: writing every column is
// what it is for, and this pins that it stayed that way.
func TestStoreDocument_StillReplacesWhatItSupplies(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	_, err := f.svc.UpdateDocumentTitle(ctx, doc, "Set By Title Call", nil)
	require.NoError(t, err)

	_, err = f.svc.StoreDocument(ctx, cat, nil, slug,
		"# Restored\n\n## Heading\nrewritten body", true, "seed", nil, nil)
	require.NoError(t, err)

	after := loadDoc(t, f, doc)
	require.Equal(t, doc, after.ID, "the same document is rewritten, not replaced")
	require.Equal(t, "Restored", after.Title,
		"authoring the whole document does set the title it supplies")
}
