//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
)

// TestGetRelatedLeak_MemberOfT1NeverSeesT2 is the load-bearing read-path leak
// guard for get_related: a member of ONLY tenant A, asking for documents related
// to a doc they can read, must never receive a related result whose owning tenant
// is B (a tenant they cannot read). A's readable set is {A, common}; B is absent,
// so no B-owned document may surface. The common-pool candidate proves the query
// still returns real results, so the exclusion of B is non-vacuous.
func TestGetRelatedLeak_MemberOfT1NeverSeesT2(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA) // member of A only

	res, err := f.svc.GetRelated(ctx, f.docA, 50, nil)
	require.NoError(t, err, "own-tenant get_related must succeed")

	var sawReadable bool
	for _, r := range res {
		require.NotEqual(t, f.tenantB, r.TenantID,
			"a T1-only member must never get a T2-owned related result")
		require.NotEqual(t, f.docB, r.DocumentID, "tenant B doc must not leak via get_related")
		require.NotEqual(t, f.docB2, r.DocumentID, "tenant B doc must not leak via get_related")
		if r.DocumentID == f.docC || r.DocumentID == f.docC2 {
			sawReadable = true
		}
	}
	require.True(t, sawReadable,
		"get_related still returns readable common-pool candidates (non-vacuous leak check)")
}

// TestGetRelatedPositive_SpansHomeAndMemberTenant proves the aggregation: a
// subject who is a member of both A (home) and B receives related results drawn
// from BOTH tenants, each labeled with its owning tenant name.
func TestGetRelatedPositive_SpansHomeAndMemberTenant(t *testing.T) {
	f := newAuthzFixture(t)
	// Grant subjA membership of tenant B so its readable set spans A + common + B.
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	caller := ctxFor(f.tenantA, f.subjA)
	// Home tenant A has only docA (the target, excluded), so seed a second A doc
	// to give the home tenant a candidate to contribute to the related set.
	docA2, _ := f.storeDoc(t, caller, nil)

	res, err := f.svc.GetRelated(caller, f.docA, 50, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res, "aggregated get_related returns candidates across the readable set")

	owners := make(map[uuid.UUID]bool)
	var sawA2, sawB bool
	for _, r := range res {
		owners[r.TenantID] = true
		require.NotEmpty(t, r.TenantName, "each related result is labeled with its owning tenant name")
		if r.DocumentID == docA2 {
			sawA2 = true
		}
		if r.DocumentID == f.docB || r.DocumentID == f.docB2 {
			sawB = true
		}
	}
	require.True(t, owners[f.tenantA], "related results include a home-tenant (A) doc")
	require.True(t, owners[f.tenantB], "related results include a member-tenant (B) doc")
	require.True(t, sawA2, "the seeded home-tenant doc is present in the related set")
	require.True(t, sawB, "a tenant-B doc is present in the related set")
}
