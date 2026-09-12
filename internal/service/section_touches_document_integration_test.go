//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// docUpdatedAt reads a document's persisted updated_at directly.
func docUpdatedAt(t *testing.T, f *authzFixture, id uuid.UUID) time.Time {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, id).Error)
	return d.UpdatedAt
}

// docSectionIDs lists a document's sections, so a test can delete one and keep
// another.
func docSectionIDs(t *testing.T, f *authzFixture, docID uuid.UUID) []uuid.UUID {
	t.Helper()
	var secs []models.Section
	require.NoError(t, f.db.Where("document_id = ?", docID).Find(&secs).Error)
	out := make([]uuid.UUID, 0, len(secs))
	for i := range secs {
		out = append(out, secs[i].ID)
	}
	return out
}

// The regression: a consumer polling updated_at to decide whether to re-read a
// document never saw a section edit, because the edit only touched the section.
func TestUpdateSection_AdvancesDocumentUpdatedAt(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, sec := f.storeDoc(t, ctx, nil)

	before := docUpdatedAt(t, f, doc)
	time.Sleep(10 * time.Millisecond)

	body := "revised content"
	_, err := f.svc.UpdateSection(ctx, sec, &body, nil, false, nil)
	require.NoError(t, err)

	after := docUpdatedAt(t, f, doc)
	require.True(t, after.After(before),
		"a section edit changes what the document says, so its updated_at must move")
}

// A heading is content too — renaming one changes the document a reader gets.
func TestUpdateSection_HeadingOnlyEditAdvancesDocumentUpdatedAt(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, sec := f.storeDoc(t, ctx, nil)

	before := docUpdatedAt(t, f, doc)
	time.Sleep(10 * time.Millisecond)

	heading := "A Renamed Heading"
	_, err := f.svc.UpdateSection(ctx, sec, nil, &heading, false, nil)
	require.NoError(t, err)

	require.True(t, docUpdatedAt(t, f, doc).After(before),
		"a heading-only edit still changes the document")
}

// Removing one of several sections leaves a document that says something
// different, so its timestamp has to move too.
func TestDeleteSection_AdvancesDocumentUpdatedAt(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	slug := "d" + uuid.NewString()
	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug,
		"# Title\n\n## First\nfirst body\n\n## Second\nsecond body", true, "seed", nil, nil)
	require.NoError(t, err)
	doc := res.Document.ID

	ids := docSectionIDs(t, f, doc)
	require.Len(t, ids, 2, "this must be a partial delete, not the last-section case")

	before := docUpdatedAt(t, f, doc)
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, f.svc.DeleteSection(ctx, ids[0], nil))

	require.True(t, docUpdatedAt(t, f, doc).After(before),
		"the document lost a section; a reader must be able to tell")
}

// The document is deleted with its last section, so there is nothing to stamp —
// the stamp must be skipped rather than failing against a vanished row.
func TestDeleteSection_LastSectionRemovesDocumentWithoutError(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, sec := f.storeDoc(t, ctx, nil)

	require.NoError(t, f.svc.DeleteSection(ctx, sec, nil))

	var count int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", doc).Count(&count).Error)
	require.Zero(t, count, "the empty document is deleted, not stamped")
}
