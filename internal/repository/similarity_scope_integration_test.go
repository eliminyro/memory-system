//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestFindSimilarDocuments_ExcludesCommonPool guards B9: the write-time duplicate
// guard (FindSimilarDocuments) must compare only against the WRITE tenant's own
// docs, never the shared common (bootstrap) pool — a caller can't edit or merge a
// common-pool doc, so flagging it as a duplicate would block a legitimate write.
func TestFindSimilarDocuments_ExcludesCommonPool(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(9))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	emb := randUnit(rng)

	// A near-identical doc in the shared common pool.
	commonSlug := "b9-common-" + uuid.NewString()
	commonDoc := seedDoc(t, db, models.BootstrapTenantID, commonSlug, emb)
	t.Cleanup(func() { db.Exec("DELETE FROM documents WHERE id = ?", commonDoc) })

	sr := repository.NewSectionRepository(db)
	ctx := context.Background()
	query := pgvector.NewVector(emb)
	// An exclude path that matches no seeded doc, so only tenant scope decides.
	excludeSlug := "b9-writing-" + uuid.NewString()

	// Only the common pool holds a match — the dup guard must find NOTHING.
	cands, err := sr.FindSimilarDocuments(ctx, tenantID, query, 0.5, 5, "learnings", nil, excludeSlug)
	require.NoError(t, err)
	require.Empty(t, cands, "common-pool near-duplicate must not be flagged by the write-tenant dup guard")

	// Positive control: an OWN-tenant near-duplicate is still flagged.
	ownSlug := "b9-own-" + uuid.NewString()
	own := seedDoc(t, db, tenantID, ownSlug, emb)
	cands, err = sr.FindSimilarDocuments(ctx, tenantID, query, 0.5, 5, "learnings", nil, excludeSlug)
	require.NoError(t, err)
	found := false
	for _, c := range cands {
		require.NotEqual(t, commonDoc, c.DocumentID, "common-pool doc leaked into the dup guard")
		if c.DocumentID == own {
			found = true
		}
	}
	require.True(t, found, "own-tenant near-duplicate should be flagged")
}
