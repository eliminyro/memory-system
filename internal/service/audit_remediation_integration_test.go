//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestEdgeList_OutOfScopeSiblingRedacted (F10): a doc-only guest (viewer on docA
// via a per-document grant, no tenant-A membership) must not see a tenant-A sibling
// through docA's edges — the out-of-scope endpoint is omitted, not leaked.
func TestEdgeList_OutOfScopeSiblingRedacted(t *testing.T) {
	f := newAuthzFixture(t)
	ownerCtx := ctxFor(f.tenantA, f.subjA)

	sibling, _ := f.storeDoc(t, ownerCtx, nil)
	_, err := f.svc.CreateEdge(ownerCtx, f.docA, sibling, models.EdgeRelatesTo, nil)
	require.NoError(t, err)

	// The tenant-A owner sees the sibling endpoint's identity.
	ownerList, err := f.svc.ListDocumentEdges(ownerCtx, f.docA, nil)
	require.NoError(t, err)
	require.Len(t, ownerList, 1)
	require.Equal(t, sibling, ownerList[0].OtherDocumentID)
	require.NotEmpty(t, ownerList[0].OtherDocumentPath, "owner sees the sibling path")

	// subjB is a member of tenant B only; grant it viewer on docA (a doc-only guest).
	require.NoError(t, f.store.Write(context.Background(), authzseed.DocumentViewer(f.docA, f.subjB)))
	guestCtx := ctxFor(f.tenantB, f.subjB)

	guestList, err := f.svc.ListDocumentEdges(guestCtx, f.docA, nil)
	require.NoError(t, err)
	require.Empty(t, guestList, "the tenant-A sibling is out of the guest's read scope and must be omitted")
}

// TestReadScope_AdminCrossTenantReadAudited (N2): an admin narrowing to a tenant
// outside their own scope writes a cross_tenant_read override_log row, while a
// same-tenant read writes none.
func TestReadScope_AdminCrossTenantReadAudited(t *testing.T) {
	f := newAuthzFixture(t)

	crossReads := func() int64 {
		var n int64
		require.NoError(t, f.db.Model(&models.OverrideLog{}).
			Where("override_type = ?", models.OverrideTypeCrossTenantRead).Count(&n).Error)
		return n
	}
	before := crossReads()

	// Admin (home tenant A, no membership in B) narrows the read to tenant B.
	_, err := f.svc.Search(ctxFor(f.tenantA, f.admin), "some body text",
		nil, nil, nil, 5, false, "", &f.tenantB, false)
	require.NoError(t, err)
	require.Equal(t, before+1, crossReads(), "admin cross-tenant read must be audited")

	// A same-tenant read (member, no override) writes no cross_tenant_read row.
	_, err = f.svc.Search(ctxFor(f.tenantA, f.subjA), "some body text",
		nil, nil, nil, 5, false, "", nil, false)
	require.NoError(t, err)
	require.Equal(t, before+1, crossReads(), "same-tenant read must not be audited")
}
