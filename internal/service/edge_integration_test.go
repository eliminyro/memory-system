//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// edgeArchived reports the archived state + reason of a doc, ignoring the normal
// archived_at read filter (tests need to see the archived row directly).
func edgeArchived(t *testing.T, f *authzFixture, id uuid.UUID) (bool, string) {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, id).Error)
	return d.ArchivedAt != nil, d.ArchiveReason
}

// TestEdgeCreate_RelatesToNoArchive: a relates_to edge stores actor + timestamp
// and archives nothing.
func TestEdgeCreate_RelatesToNoArchive(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	res, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)
	require.False(t, res.TargetArchived)
	require.Equal(t, models.EdgeRelatesTo, res.Edge.EdgeType)
	require.Equal(t, f.subjA, res.Edge.ActorSubject)
	require.False(t, res.Edge.CreatedAt.IsZero(), "creation timestamp is recorded")

	archived, _ := edgeArchived(t, f, tgt)
	require.False(t, archived, "relates_to archives neither endpoint")
}

// TestEdgeCreate_Rejections: unknown type, self-edge, and cross-tenant are refused.
func TestEdgeCreate_Rejections(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	_, err := f.svc.CreateEdge(ctx, src, tgt, "bogus", nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "unknown edge type rejected")

	_, err = f.svc.CreateEdge(ctx, src, src, models.EdgeRelatesTo, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "self-edge rejected")

	// src is in tenant A, docC is in the common (bootstrap) pool — different tenants.
	_, err = f.svc.CreateEdge(ctx, src, f.docC, models.EdgeRelatesTo, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "cross-tenant edge rejected")
}

// TestEdgeCreate_DuplicateIdempotent: re-creating an identical edge returns the
// existing one and adds no second row.
func TestEdgeCreate_DuplicateIdempotent(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	first, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)
	second, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)
	require.Equal(t, first.Edge.ID, second.Edge.ID, "duplicate returns the existing edge")

	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Len(t, list, 1, "no second row created")
}

// TestEdgeSupersede_ArchivesTargetAtomically: supersedes archives the active
// target with reason=superseded and stores the edge together.
func TestEdgeSupersede_ArchivesTargetAtomically(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	res, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeSupersedes, nil)
	require.NoError(t, err)
	require.True(t, res.TargetArchived)

	archived, reason := edgeArchived(t, f, tgt)
	require.True(t, archived, "target archived")
	require.Equal(t, models.ArchiveReasonSuperseded, reason)

	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, models.EdgeSupersedes, list[0].EdgeType)
}

// TestEdgeSupersede_AlreadyArchivedTarget: a new supersede edge onto an already
// archived target is stored with no error and leaves its state unchanged.
func TestEdgeSupersede_AlreadyArchivedTarget(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src1, _ := f.storeDoc(t, ctx, nil)
	src2, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	_, err := f.svc.CreateEdge(ctx, src1, tgt, models.EdgeSupersedes, nil)
	require.NoError(t, err)

	res, err := f.svc.CreateEdge(ctx, src2, tgt, models.EdgeSupersedes, nil)
	require.NoError(t, err, "superseding an already-archived target is not an error")
	require.False(t, res.TargetArchived, "no second archive is performed")

	archived, reason := edgeArchived(t, f, tgt)
	require.True(t, archived)
	require.Equal(t, models.ArchiveReasonSuperseded, reason)
}

// TestEdgeSupersede_RollbackOnFailure: a failed in-tx archive rolls back the
// edge insert too — proving both writes share one tx (archive on a non-tx
// handle would leave the edge committed and turn this test red).
func TestEdgeSupersede_RollbackOnFailure(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	// Force the archive UPDATE on documents to fail from inside the create tx.
	f.db.Exec(`DROP TRIGGER IF EXISTS edge_block_update ON documents`)
	require.NoError(t, f.db.Exec(`CREATE OR REPLACE FUNCTION edge_block_update() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'blocked'; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER edge_block_update BEFORE UPDATE ON documents FOR EACH ROW EXECUTE FUNCTION edge_block_update()`).Error)
	defer func() {
		f.db.Exec(`DROP TRIGGER IF EXISTS edge_block_update ON documents`)
		f.db.Exec(`DROP FUNCTION IF EXISTS edge_block_update()`)
	}()

	_, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeSupersedes, nil)
	require.Error(t, err, "a failed archive fails the whole create")

	// Remove the block so the assertions below can read the state freely.
	require.NoError(t, f.db.Exec(`DROP TRIGGER IF EXISTS edge_block_update ON documents`).Error)

	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Empty(t, list, "edge insert rolled back with the failed archive")

	archived, _ := edgeArchived(t, f, tgt)
	require.False(t, archived, "target left un-archived")
}

// TestEdgeAuthz_Supersede: editor on source alone cannot supersede; editor on
// both can; viewer on the target suffices for a non-archiving relates_to.
func TestEdgeAuthz_Supersede(t *testing.T) {
	f := newAuthzFixture(t)
	owner := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, owner, nil)
	tgtView, _ := f.storeDoc(t, owner, nil)
	tgtEdit, _ := f.storeDoc(t, owner, nil)
	tgtRel, _ := f.storeDoc(t, owner, nil)

	// A subject with only per-doc grants (not a tenant member), acting in tenant A.
	lim := "userLim-" + uuid.NewString()
	limCtx := ctxFor(f.tenantA, lim)
	bg := context.Background()
	require.NoError(t, f.store.Write(bg, authzseed.DocumentEditor(src, lim)))
	require.NoError(t, f.store.Write(bg, authzseed.DocumentViewer(tgtView, lim)))
	require.NoError(t, f.store.Write(bg, authzseed.DocumentEditor(tgtEdit, lim)))
	require.NoError(t, f.store.Write(bg, authzseed.DocumentViewer(tgtRel, lim)))

	// editor-on-source, viewer-on-target: supersede refused, nothing written.
	_, err := f.svc.CreateEdge(limCtx, src, tgtView, models.EdgeSupersedes, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "viewer-only target cannot be superseded")
	archived, _ := edgeArchived(t, f, tgtView)
	require.False(t, archived, "denied supersede archives nothing")
	list, err := f.svc.ListDocumentEdges(owner, src, nil)
	require.NoError(t, err)
	require.Empty(t, list, "denied supersede writes no edge")

	// editor on both: supersede succeeds.
	res, err := f.svc.CreateEdge(limCtx, src, tgtEdit, models.EdgeSupersedes, nil)
	require.NoError(t, err)
	require.True(t, res.TargetArchived)

	// viewer on target suffices for relates_to (no archive).
	res, err = f.svc.CreateEdge(limCtx, src, tgtRel, models.EdgeRelatesTo, nil)
	require.NoError(t, err)
	require.False(t, res.TargetArchived)
	archived, _ = edgeArchived(t, f, tgtRel)
	require.False(t, archived)
}

// TestEdgeList_NonReaderRefused: a caller who cannot read the doc gets a refusal
// and no edge data.
func TestEdgeList_NonReaderRefused(t *testing.T) {
	f := newAuthzFixture(t)
	owner := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, owner, nil)
	tgt, _ := f.storeDoc(t, owner, nil)
	_, err := f.svc.CreateEdge(owner, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)

	// subjB (tenant B) cannot read tenant A's document. The refusal mirrors the
	// sibling related-view read (GetRelated): a clean ErrInvalidInput, never a
	// leaky not-found-vs-forbidden distinction.
	other := ctxFor(f.tenantB, f.subjB)
	list, err := f.svc.ListDocumentEdges(other, src, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "non-reader refused with the sibling read's error")
	require.Empty(t, list, "no edge data is revealed")
}

// TestEdgeList_BothDirectionsAndArchived: listing returns outgoing + incoming
// edges, and an outgoing supersede to an archived target is flagged archived.
func TestEdgeList_BothDirectionsAndArchived(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	mid, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	_, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeSupersedes, nil) // outgoing, archives tgt
	require.NoError(t, err)
	_, err = f.svc.CreateEdge(ctx, mid, src, models.EdgeDerivedFrom, nil) // incoming to src
	require.NoError(t, err)

	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Len(t, list, 2)

	byDir := map[string]bool{}
	for _, e := range list {
		byDir[e.Direction] = true
		if e.Direction == "outgoing" {
			require.Equal(t, tgt, e.OtherDocumentID)
			require.True(t, e.OtherDocumentArchived, "supersede target is archived and still listed")
		} else {
			require.Equal(t, mid, e.OtherDocumentID)
		}
	}
	require.True(t, byDir["outgoing"] && byDir["incoming"], "both directions present")
}

// TestEdgeDelete: an edge is removed from listing; deleting a supersede edge does
// NOT un-archive its target; a non-editor cannot delete.
func TestEdgeDelete(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)

	rel, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)

	// A non-editor (tenant B) cannot delete.
	require.Error(t, f.svc.DeleteEdge(ctxFor(f.tenantB, f.subjB), rel.Edge.ID, nil))

	require.NoError(t, f.svc.DeleteEdge(ctx, rel.Edge.ID, nil))
	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Empty(t, list, "deleted edge gone from listing")

	// Deleting a supersede edge leaves the target archived.
	tgt2, _ := f.storeDoc(t, ctx, nil)
	sup, err := f.svc.CreateEdge(ctx, src, tgt2, models.EdgeSupersedes, nil)
	require.NoError(t, err)
	require.NoError(t, f.svc.DeleteEdge(ctx, sup.Edge.ID, nil))
	archived, _ := edgeArchived(t, f, tgt2)
	require.True(t, archived, "delete does not un-archive the former target")
}

// TestEdge_NoEdgesNoChange: with no edges, listing is empty and get_related still
// works — the overlay is inert until an edge is created.
func TestEdge_NoEdgesNoChange(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)

	list, err := f.svc.ListDocumentEdges(ctx, src, nil)
	require.NoError(t, err)
	require.Empty(t, list)

	_, err = f.svc.GetRelated(ctx, src, 5, nil)
	require.NoError(t, err, "similarity path is unchanged with no edges")
}
