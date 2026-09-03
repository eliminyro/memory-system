//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestGenerateIndex_ScopeFiltersTenants exercises the repo-layer contract for the
// tenant-set GenerateIndex: the catalog is built only from documents whose
// tenant_id is in the passed set (tenant_id IN (?)), so an out-of-scope tenant's
// categories can never surface. Widening the set makes the previously-excluded
// tenant appear, and the category filter still narrows within the set.
func TestGenerateIndex_ScopeFiltersTenants(t *testing.T) {
	db := openLintPG(t)
	repo := repository.NewDocumentRepository(db)
	ctx := context.Background()

	tA, tB := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&models.Tenant{ID: tA, Name: "A-" + uuid.NewString()}).Error)
	require.NoError(t, db.Create(&models.Tenant{ID: tB, Name: "B-" + uuid.NewString()}).Error)

	// Distinct, per-run categories so a shared test DB can never contaminate the
	// membership assertions.
	catA := "cata-" + uuid.NewString()
	catB := "catb-" + uuid.NewString()
	require.NoError(t, db.Create(&models.Document{
		ID: uuid.New(), TenantID: tA, Category: catA, Slug: "s" + uuid.NewString(), Title: "TitleA", DocType: "learning",
	}).Error)
	require.NoError(t, db.Create(&models.Document{
		ID: uuid.New(), TenantID: tB, Category: catB, Slug: "s" + uuid.NewString(), Title: "TitleB", DocType: "learning",
	}).Error)

	catsOf := func(entries []repository.IndexEntry) map[string]bool {
		m := make(map[string]bool)
		for _, e := range entries {
			m[e.Category] = true
		}
		return m
	}

	// Scope to A only: B's category must be excluded by tenant_id IN (?).
	res, err := repo.GenerateIndex(ctx, []uuid.UUID{tA}, repository.IndexDepthSummary, nil)
	require.NoError(t, err)
	gotA := catsOf(res)
	require.True(t, gotA[catA], "in-scope tenant category is present")
	require.False(t, gotA[catB], "out-of-scope tenant category must be filtered by tenant_id IN (?)")

	// Widen to A + B: the B-owned category now appears.
	res2, err := repo.GenerateIndex(ctx, []uuid.UUID{tA, tB}, repository.IndexDepthSummary, nil)
	require.NoError(t, err)
	gotAB := catsOf(res2)
	require.True(t, gotAB[catA], "A category present in the widened scope")
	require.True(t, gotAB[catB], "widened scope includes the B-owned category")

	// The category filter still narrows within the set (depth semantics preserved).
	res3, err := repo.GenerateIndex(ctx, []uuid.UUID{tA, tB}, repository.IndexDepthSummary, &catB)
	require.NoError(t, err)
	gotFiltered := catsOf(res3)
	require.True(t, gotFiltered[catB], "category filter keeps the matching category")
	require.False(t, gotFiltered[catA], "category filter excludes non-matching categories")
}

// TestGenerateIndex_SummarySplitsByTenant locks the per-tenant summary contract:
// the same category living in two tenants yields ONE row per tenant, each
// carrying its own tenant id/name and its own doc count (not one merged row).
func TestGenerateIndex_SummarySplitsByTenant(t *testing.T) {
	db := openLintPG(t)
	repo := repository.NewDocumentRepository(db)
	ctx := context.Background()

	tA, tB := uuid.New(), uuid.New()
	nameA, nameB := "A-"+uuid.NewString(), "B-"+uuid.NewString()
	require.NoError(t, db.Create(&models.Tenant{ID: tA, Name: nameA}).Error)
	require.NoError(t, db.Create(&models.Tenant{ID: tB, Name: nameB}).Error)

	cat := "shared-cat-" + uuid.NewString() // same category in BOTH tenants
	require.NoError(t, db.Create(&models.Document{ID: uuid.New(), TenantID: tA, Category: cat, Slug: "s" + uuid.NewString(), Title: "A1", DocType: "learning"}).Error)
	require.NoError(t, db.Create(&models.Document{ID: uuid.New(), TenantID: tA, Category: cat, Slug: "s" + uuid.NewString(), Title: "A2", DocType: "learning"}).Error)
	require.NoError(t, db.Create(&models.Document{ID: uuid.New(), TenantID: tB, Category: cat, Slug: "s" + uuid.NewString(), Title: "B1", DocType: "learning"}).Error)

	res, err := repo.GenerateIndex(ctx, []uuid.UUID{tA, tB}, repository.IndexDepthSummary, &cat)
	require.NoError(t, err)

	byTenant := make(map[uuid.UUID]repository.IndexEntry)
	for _, e := range res {
		if e.Category == cat {
			byTenant[e.TenantID] = e
		}
	}
	require.Len(t, byTenant, 2, "the shared category splits into one summary row per tenant")
	require.Equal(t, nameA, byTenant[tA].TenantName, "row carries its tenant's name")
	require.Equal(t, 2, byTenant[tA].DocCount, "per-tenant doc count, not a merged total")
	require.Equal(t, nameB, byTenant[tB].TenantName)
	require.Equal(t, 1, byTenant[tB].DocCount)
}
