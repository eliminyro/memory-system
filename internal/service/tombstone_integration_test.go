//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// tombstoneSectionCount returns how many sections a document currently has,
// reading the table directly (bypassing the archived read filter).
func tombstoneSectionCount(t *testing.T, f *authzFixture, docID uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.Section{}).Where("document_id = ?", docID).Count(&n).Error)
	return n
}

// TestTombstone_SupersedePurgesTargetSections: superseding archives the target
// AND deletes its sections in the same tx (tx atomicity is proven separately by
// TestEdgeSupersede_RollbackOnFailure); the successor keeps its own content.
func TestTombstone_SupersedePurgesTargetSections(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	successor, _ := f.storeDoc(t, ctx, nil)
	target, _ := f.storeDoc(t, ctx, nil)

	require.Positive(t, tombstoneSectionCount(t, f, target), "target starts with content")

	res, err := f.svc.CreateEdge(ctx, successor, target, models.EdgeSupersedes, nil)
	require.NoError(t, err)
	require.True(t, res.TargetArchived)

	archived, reason := edgeArchived(t, f, target)
	require.True(t, archived, "target archived")
	require.Equal(t, models.ArchiveReasonSuperseded, reason)
	require.Zero(t, tombstoneSectionCount(t, f, target), "target sections purged")

	require.Positive(t, tombstoneSectionCount(t, f, successor), "successor content untouched")
}

// TestTombstone_NonSupersedeEdgeDoesNotPurge: a non-supersede edge never archives
// or purges its target — locking the purge to the supersede path.
func TestTombstone_NonSupersedeEdgeDoesNotPurge(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	source, _ := f.storeDoc(t, ctx, nil)
	target, _ := f.storeDoc(t, ctx, nil)
	before := tombstoneSectionCount(t, f, target)
	require.Positive(t, before, "target starts with content")

	res, err := f.svc.CreateEdge(ctx, source, target, models.EdgeRelatesTo, nil)
	require.NoError(t, err)
	require.False(t, res.TargetArchived, "non-supersede edge must not archive")

	archived, _ := edgeArchived(t, f, target)
	require.False(t, archived, "target must not be archived")
	require.Equal(t, before, tombstoneSectionCount(t, f, target), "sections untouched")
}

// TestTombstone_ResolvesAsEdgeButAbsentFromSearchAndReads: a tombstone still
// resolves as an edge endpoint (path/title/archived) yet is gone from search and
// direct reads.
func TestTombstone_ResolvesAsEdgeButAbsentFromSearchAndReads(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	successor, _ := f.storeDoc(t, ctx, nil)
	target, _ := f.storeDoc(t, ctx, nil)
	cat, slug := f.catSlug(t, target)

	_, err := f.svc.CreateEdge(ctx, successor, target, models.EdgeSupersedes, nil)
	require.NoError(t, err)

	list, err := f.svc.ListDocumentEdges(ctx, successor, nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, target, list[0].OtherDocumentID)
	require.True(t, list[0].OtherDocumentArchived, "tombstone flagged archived in edge listing")
	require.NotEmpty(t, list[0].OtherDocumentPath, "tombstone path preserved")
	require.NotEmpty(t, list[0].OtherDocumentTitle, "tombstone title preserved")

	_, err = f.svc.GetDocument(ctx, cat, nil, slug, false, "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "tombstone is not readable")

	results, err := f.svc.Search(ctx, "some body text", nil, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	for _, r := range results {
		require.NotEqual(t, target, r.DocumentID, "tombstone content absent from search")
	}
}

// TestTombstone_ReSupersedeIsNoOp: superseding an already-archived tombstone from
// a new source archives nothing and purges nothing, without error.
func TestTombstone_ReSupersedeIsNoOp(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src1, _ := f.storeDoc(t, ctx, nil)
	src2, _ := f.storeDoc(t, ctx, nil)
	target, _ := f.storeDoc(t, ctx, nil)

	res1, err := f.svc.CreateEdge(ctx, src1, target, models.EdgeSupersedes, nil)
	require.NoError(t, err)
	require.True(t, res1.TargetArchived)
	require.Zero(t, tombstoneSectionCount(t, f, target), "first supersede purges")

	res2, err := f.svc.CreateEdge(ctx, src2, target, models.EdgeSupersedes, nil)
	require.NoError(t, err, "re-supersede of a tombstone is not an error")
	require.False(t, res2.TargetArchived, "no second archive")
	require.Zero(t, tombstoneSectionCount(t, f, target), "still zero sections")

	archived, reason := edgeArchived(t, f, target)
	require.True(t, archived)
	require.Equal(t, models.ArchiveReasonSuperseded, reason)
}
