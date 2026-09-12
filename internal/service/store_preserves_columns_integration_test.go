//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// storedDoc reads a document row directly, for columns the service does not
// return.
func storedDoc(t *testing.T, f *authzFixture, id uuid.UUID) models.Document {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, id).Error)
	return d
}

// A pinned document stays pinned across an overwrite. Before this change that
// held only because a copy-back line said so.
func TestStoreDocument_OverwritePreservesPinned(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	yes := true
	_, err := f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\npinned body", true, "seed", nil, &yes, nil)
	require.NoError(t, err)
	require.True(t, storedDoc(t, f, doc).Pinned, "precondition: the document is pinned")

	_, err = f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\nlater body", true, "seed", nil, nil, nil)
	require.NoError(t, err)

	require.True(t, storedDoc(t, f, doc).Pinned,
		"an overwrite that says nothing about pinning must not unpin")
}

// doc_type carries an admin override, so an overwrite must not revert it to the
// type inferred from the path.
func TestStoreDocument_OverwritePreservesDocTypeOverride(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", doc).
		Update("doc_type", "audit").Error)

	_, err := f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\nlater body", true, "seed", nil, nil, nil)
	require.NoError(t, err)

	require.Equal(t, "audit", storedDoc(t, f, doc).DocType,
		"the inferred type must not overwrite an admin's override")
}

// The other half: what the call does supply still lands.
func TestStoreDocument_OverwriteStillAppliesWhatItSupplies(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	before := storedDoc(t, f, doc)

	_, err := f.svc.StoreDocumentScoped(ctx, cat, nil, slug,
		"# Renamed\n\n## H\ncompletely new body", true, "seed", nil, nil, nil)
	require.NoError(t, err)

	after := storedDoc(t, f, doc)
	require.Equal(t, "Renamed", after.Title, "the supplied title is applied")
	require.NotEqual(t, before.ContentHash, after.ContentHash, "the content hash tracks the new body")
	require.Equal(t, before.ID, after.ID, "the same row is overwritten")
	require.Equal(t, before.CreatedAt.UnixMicro(), after.CreatedAt.UnixMicro(), "creation time is the row's own")
}

// scope: unset preserves, set replaces. The polarity flipped in the rewrite, so
// both directions are worth pinning.
func TestStoreDocument_OverwriteScopeUnsetPreservesSetReplaces(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	first := "apps/**"
	_, err := f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\nb", true, "seed", nil, nil, &first)
	require.NoError(t, err)
	require.NotNil(t, storedDoc(t, f, doc).Scope)
	require.Equal(t, first, *storedDoc(t, f, doc).Scope)

	_, err = f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\nc", true, "seed", nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, storedDoc(t, f, doc).Scope, "unset scope preserves")
	require.Equal(t, first, *storedDoc(t, f, doc).Scope)

	second := "libs/**"
	_, err = f.svc.StoreDocumentScoped(ctx, cat, nil, slug, "# T\n\n## H\nd", true, "seed", nil, nil, &second)
	require.NoError(t, err)
	require.Equal(t, second, *storedDoc(t, f, doc).Scope, "a supplied scope replaces")
}

// doc.Sections is now nilled before the save, and writeSections is fed a
// captured copy. If that capture were dropped the rewrite would silently stop
// rewriting sections.
func TestStoreDocument_OverwriteStillRewritesSections(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	doc, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, doc)

	res, err := f.svc.StoreDocumentScoped(ctx, cat, nil, slug,
		"# T\n\n## First\none\n\n## Second\ntwo", true, "seed", nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Document.Sections, 2, "both sections come back")

	var count int64
	require.NoError(t, f.db.Model(&models.Section{}).Where("document_id = ?", doc).Count(&count).Error)
	require.EqualValues(t, 2, count, "and both are persisted")
}
