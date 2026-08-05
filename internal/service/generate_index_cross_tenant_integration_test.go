//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// seedIndexDoc inserts a bare document with a caller-chosen category into a
// tenant. GenerateIndex reads only category/subcategory/title/tenant_id, so a
// row (no section/embedding) is enough; the tenant scope is decided by the
// caller's membership tuples, not per-doc authz.
func seedIndexDoc(t *testing.T, f *authzFixture, tenant uuid.UUID, category string) {
	t.Helper()
	require.NoError(t, f.db.Create(&models.Document{
		ID:       uuid.New(),
		TenantID: tenant,
		Category: category,
		Slug:     "s" + uuid.NewString(),
		Title:    "Idx",
		DocType:  "learning",
	}).Error)
}

func indexCategories(entries []repository.IndexEntry) map[string]bool {
	m := make(map[string]bool)
	for _, e := range entries {
		m[e.Category] = true
	}
	return m
}

// TestGenerateIndexLeak_MemberOfT1NeverSeesT2 is the read-path leak guard for
// generate_index: a member of ONLY tenant A gets a catalog that reflects A + the
// common pool but never a category owned solely by tenant B (which A cannot
// read). The home category proves the exclusion is non-vacuous.
func TestGenerateIndexLeak_MemberOfT1NeverSeesT2(t *testing.T) {
	f := newAuthzFixture(t)
	homeCat := "home-" + uuid.NewString() // tenant A (readable)
	leakCat := "leak-" + uuid.NewString() // tenant B (NOT readable by an A-only member)
	seedIndexDoc(t, f, f.tenantA, homeCat)
	seedIndexDoc(t, f, f.tenantB, leakCat)

	ctx := ctxFor(f.tenantA, f.subjA) // member of A only
	entries, err := f.svc.GenerateIndex(ctx, "summary", nil, nil)
	require.NoError(t, err)

	cats := indexCategories(entries)
	require.True(t, cats[homeCat], "own-tenant category present (non-vacuous leak check)")
	require.False(t, cats[leakCat], "a T1-only member's index must never reflect a T2 category")
}

// TestGenerateIndexPositive_SpansHomeAndMemberTenant proves the aggregation: a
// subject who is a member of both A (home) and B gets a catalog spanning both.
func TestGenerateIndexPositive_SpansHomeAndMemberTenant(t *testing.T) {
	f := newAuthzFixture(t)
	// Grant subjA membership of tenant B so its readable set spans A + common + B.
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	homeCat := "home-" + uuid.NewString()
	memberCat := "member-" + uuid.NewString()
	seedIndexDoc(t, f, f.tenantA, homeCat)
	seedIndexDoc(t, f, f.tenantB, memberCat)

	ctx := ctxFor(f.tenantA, f.subjA)
	entries, err := f.svc.GenerateIndex(ctx, "summary", nil, nil)
	require.NoError(t, err)

	cats := indexCategories(entries)
	require.True(t, cats[homeCat], "index includes a home-tenant category")
	require.True(t, cats[memberCat], "index spans the member tenant")
}

// TestGenerateIndexFilter_NarrowsAndNonReadableEmpty covers the tenant_id filter:
// a readable filter narrows to exactly that tenant; a non-readable filter yields
// an empty index (empty scope), never the target tenant's documents.
func TestGenerateIndexFilter_NarrowsAndNonReadableEmpty(t *testing.T) {
	f := newAuthzFixture(t)
	catA := "a-" + uuid.NewString()
	catB := "b-" + uuid.NewString()
	seedIndexDoc(t, f, f.tenantA, catA)
	seedIndexDoc(t, f, f.tenantB, catB)

	// Admin may read any tenant; the tenant_id filter narrows to exactly B.
	admin := ctxFor(f.tenantA, f.admin)
	entries, err := f.svc.GenerateIndex(admin, "summary", nil, &f.tenantB)
	require.NoError(t, err)
	cats := indexCategories(entries)
	require.True(t, cats[catB], "filter narrows to the requested tenant's docs")
	require.False(t, cats[catA], "the filter excludes the caller's home tenant")

	// A non-readable tenant_id -> empty scope -> empty index (no existence leak).
	member := ctxFor(f.tenantA, f.subjA) // member of A only, cannot read B
	empty, err := f.svc.GenerateIndex(member, "summary", nil, &f.tenantB)
	require.NoError(t, err)
	require.Empty(t, empty, "a non-readable tenant_id yields an empty index, never B's docs")
}
