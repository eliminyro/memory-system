//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/repository"
)

// vec2D builds a dupTestDim-length embedding with only the first two
// components set -- an exact 2D construction so cosine similarities between
// fixture vectors are deterministic, not statistical (unlike randUnit).
func vec2D(c0, c1 float32) []float32 {
	v := make([]float32, dupTestDim)
	v[0], v[1] = c0, c1
	return v
}

// TestHybridSearch_MMRDisplacesNearDuplicate exercises the spec's canonical
// scenario end-to-end through the real SQL: A and B are near-duplicate vector
// matches (cosine(A,B) ~= 0.99997, both outscoring C), C is a diverse,
// lower-scoring third match (cosine(A,C) ~= 0.649). This is what the pure-Go
// unit tests (section_mmr_test.go) can't catch: a broken COALESCE or column
// wire between the CTEs and hybridRow.Embedding.
func TestHybridSearch_MMRDisplacesNearDuplicate(t *testing.T) {
	db := openLintPG(t)
	repo := repository.NewSectionRepository(db)
	ctx := context.Background()

	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	// query = e1. A, B sit near e1 (cos ~0.99/0.989, tiny mutual angle) on the
	// +e2 side; C sits further out (cos 0.75) on the -e2 side, so
	// cosine(A,C) ~= 0.649 -- genuinely diverse from the A/B cluster.
	query := vec2D(1, 0)
	a := vec2D(0.99, 0.14107)
	b := vec2D(0.989, 0.14791)
	c := vec2D(0.75, -0.661438)

	slugA := "mmr-a-" + uuid.NewString()
	slugB := "mmr-b-" + uuid.NewString()
	slugC := "mmr-c-" + uuid.NewString()
	seedDoc(t, db, tenantID, slugA, a)
	seedDoc(t, db, tenantID, slugB, b)
	seedDoc(t, db, tenantID, slugC, c)

	// A query term that matches none of the seeded content (seedDoc sets
	// Content = slug), so lexical scoring never fires and each row's score is
	// pure vector similarity -- keeps the A/B/C score arithmetic exact.
	noMatchQuery := "zzz-no-lexical-match-" + uuid.NewString()

	base := repository.SearchParams{
		TenantIDs: []uuid.UUID{tenantID},
		Embedding: pgvector.NewVector(query),
		Query:     noMatchQuery,
		Limit:     2,
	}

	// Default (MMR off): the near-duplicate pair A+B (higher raw scores) fills
	// the top-2, crowding out diverse C -- the exact problem MMR fixes, and
	// proof the knob-off path is untouched by the SQL/hybridRow changes.
	def, err := repo.HybridSearch(ctx, base)
	require.NoError(t, err)
	require.Len(t, def, 2)
	defSlugs := map[string]bool{}
	for _, r := range def {
		defSlugs[r.Slug] = true
	}
	require.True(t, defSlugs[slugA] && defSlugs[slugB],
		"default path: expected near-duplicates A+B in top-2, got %v", defSlugs)

	// MMR on (diversity-weighted lambda): diversity displaces B for the lower-
	// scoring but diverse C, while A (top relevance) is still picked first. Under
	// F4 min-max normalization the RRF spread is tiny, so lambda must favor diversity.
	lambda := 0.3
	mmrParams := base
	mmrParams.MMRLambda = &lambda
	mmr, err := repo.HybridSearch(ctx, mmrParams)
	require.NoError(t, err)
	require.Len(t, mmr, 2)
	mmrSlugs := map[string]bool{}
	for _, r := range mmr {
		mmrSlugs[r.Slug] = true
	}
	require.True(t, mmrSlugs[slugA], "MMR: top relevance A should still be first pick")
	require.True(t, mmrSlugs[slugC], "MMR: diverse C should displace the near-duplicate B")
	require.False(t, mmrSlugs[slugB], "MMR: near-duplicate B should be displaced by diversity")
}
