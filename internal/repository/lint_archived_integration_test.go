//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestLintChecks_ExcludeArchived guards B12: the four lint checks that previously
// omitted the archived_at filter (CheckStale, CheckSparse, CheckEmptyCategories,
// CheckNearDuplicates) must exclude soft-deleted docs, matching every read path.
func TestLintChecks_ExcludeArchived(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(12))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	base := randUnit(rng)

	// Archived doc in a category of its own, sparse (one short section), stale
	// (backdated well past the threshold), and a near-duplicate of a live doc.
	// Absent the archived_at filter it would trip all four checks.
	archID := uuid.New()
	archCat := "b12cat-" + uuid.NewString()[:8]
	archSlug := "b12arch"
	require.NoError(t, db.Create(&models.Document{
		ID: archID, TenantID: tenantID, Category: archCat, Slug: archSlug, Title: "arch", DocType: "learning",
	}).Error)
	require.NoError(t, db.Create(&models.Section{
		DocumentID: archID, Ordinal: 0, Content: "x", Embedding: pgvector.NewVector(nearDup(base, rng, 0.001)),
	}).Error)
	require.NoError(t, db.Exec(
		`UPDATE documents SET archived_at = NOW(), updated_at = NOW() - INTERVAL '400 days' WHERE id = ?`, archID,
	).Error)
	archPath := models.BuildPath(archCat, nil, archSlug)

	// A LIVE near-duplicate partner for the archived doc: with the fix the archived
	// doc drops out of both the candidate CTE and the neighbour probe, so no pair
	// forms; without it, (live, archived) would surface as a near-duplicate.
	_ = seedDoc(t, db, tenantID, "b12-live-"+uuid.NewString(), base)

	lr := repository.NewLintRepository(db)
	th := repository.DefaultLintThresholds()
	th.DuplicateSimilarity = 0.90
	ctx := context.Background()

	assertPathAbsent := func(name string, findings []repository.LintFinding, err error) {
		require.NoErrorf(t, err, "%s errored", name)
		for _, f := range findings {
			require.NotEqualf(t, archPath, f.DocumentPath, "%s surfaced archived doc as DocumentPath", name)
			require.Falsef(t, strings.Contains(f.Message, archPath), "%s referenced archived doc in message: %s", name, f.Message)
		}
	}

	stale, err := lr.CheckStale(ctx, tenantID, th, nil)
	assertPathAbsent("CheckStale", stale, err)

	sparse, err := lr.CheckSparse(ctx, tenantID, th)
	assertPathAbsent("CheckSparse", sparse, err)

	// CheckEmptyCategories keys on category, not the doc path: the archived doc's
	// lone category must not appear (0 live docs => absent, not "only 1 document").
	empty, err := lr.CheckEmptyCategories(ctx, tenantID, th)
	require.NoError(t, err)
	for _, f := range empty {
		require.NotEqual(t, archCat, f.DocumentPath, "CheckEmptyCategories counted an archived doc's category")
	}

	dups, err := lr.CheckNearDuplicates(ctx, tenantID, th, nil)
	assertPathAbsent("CheckNearDuplicates", dups, err)
}
