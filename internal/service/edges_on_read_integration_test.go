//go:build integration

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestEdgesOnRead_RelatesToBothDirections: a get_document_by_id read carries the
// doc's edges — outgoing from the source, incoming at the target — and an edge-free
// doc reads back with none.
func TestEdgesOnRead_RelatesToBothDirections(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	tgt, _ := f.storeDoc(t, ctx, nil)
	lone, _ := f.storeDoc(t, ctx, nil)

	_, err := f.svc.CreateEdge(ctx, src, tgt, models.EdgeRelatesTo, nil)
	require.NoError(t, err)

	vsrc, err := f.svc.GetDocumentByID(ctx, src, false, "", nil)
	require.NoError(t, err)
	require.Len(t, vsrc.Edges, 1)
	require.Equal(t, models.EdgeRelatesTo, vsrc.Edges[0].EdgeType)
	require.Equal(t, "outgoing", vsrc.Edges[0].Direction)
	require.NotEmpty(t, vsrc.Edges[0].Path, "the other endpoint's path is surfaced")
	require.NotEmpty(t, vsrc.Edges[0].Title)

	vtgt, err := f.svc.GetDocumentByID(ctx, tgt, false, "", nil)
	require.NoError(t, err)
	require.Len(t, vtgt.Edges, 1)
	require.Equal(t, "incoming", vtgt.Edges[0].Direction, "same edge reads as incoming at the target")

	vlone, err := f.svc.GetDocumentByID(ctx, lone, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, vlone.Edges, "a doc with no edges carries no edge entries")
}

// TestEdgesOnRead_IncludesDiscoverableOnDefaultRead: an includes edge appears in
// the edge list on a default (non-expand) read without inlining the target's
// content, so the relationship is discoverable before deciding to expand.
func TestEdgesOnRead_IncludesDiscoverableOnDefaultRead(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	src, _ := f.storeDoc(t, ctx, nil)
	inc, _ := f.storeDoc(t, ctx, nil)

	_, err := f.svc.CreateEdge(ctx, src, inc, models.EdgeIncludes, nil)
	require.NoError(t, err)

	view, err := f.svc.GetDocumentByID(ctx, src, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Edges, 1)
	require.Equal(t, models.EdgeIncludes, view.Edges[0].EdgeType)
	require.Empty(t, view.Includes, "a default read does not inline included content")
}
