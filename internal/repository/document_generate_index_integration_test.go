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
