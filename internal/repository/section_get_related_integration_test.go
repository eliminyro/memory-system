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

// TestSectionGetRelated_ScopeAndLabel exercises the repo-layer contract for the
// tenant-set GetRelated: results are filtered to tenant_id IN tenantIDs (so an
// out-of-scope tenant can never surface) and each result carries its owning
// TenantID (the new SQL column). Identical unit embeddings make every candidate
// maximally similar, so membership — not ranking — is what the assertions test.
func TestSectionGetRelated_ScopeAndLabel(t *testing.T) {
	db := openLintPG(t)
	repo := repository.NewSectionRepository(db)
	ctx := context.Background()

	tA, tB := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&models.Tenant{ID: tA, Name: "A-" + uuid.NewString()}).Error)
	require.NoError(t, db.Create(&models.Tenant{ID: tB, Name: "B-" + uuid.NewString()}).Error)

	emb := make([]float32, dupTestDim)
	emb[0] = 1 // identical unit vector across all docs -> cosine similarity 1.0

	target := seedDoc(t, db, tA, "target-"+uuid.NewString(), emb)
	relA := seedDoc(t, db, tA, "relA-"+uuid.NewString(), emb)
	relB := seedDoc(t, db, tB, "relB-"+uuid.NewString(), emb)

	// Scope to A only: the tenant-B doc must be excluded by tenant_id IN (?).
	res, err := repo.GetRelated(ctx, []uuid.UUID{tA}, target, 50)
	require.NoError(t, err)
	ownerA := make(map[uuid.UUID]uuid.UUID) // docID -> owning tenant id (from result)
	for _, r := range res {
		ownerA[r.DocumentID] = r.TenantID
	}
	require.Contains(t, ownerA, relA, "in-scope related doc is returned")
	require.NotContains(t, ownerA, relB, "out-of-scope tenant doc must be filtered by tenant_id IN (?)")
	require.NotContains(t, ownerA, target, "the target document is excluded")
	require.Equal(t, tA, ownerA[relA], "result carries its owning tenant id")

	// Widen the scope to A + B: the B-owned doc now appears, labeled tenant B.
	res2, err := repo.GetRelated(ctx, []uuid.UUID{tA, tB}, target, 50)
	require.NoError(t, err)
	ownerAB := make(map[uuid.UUID]uuid.UUID)
	for _, r := range res2 {
		ownerAB[r.DocumentID] = r.TenantID
	}
	require.Equal(t, tA, ownerAB[relA], "A-owned doc labeled tenant A")
	require.Equal(t, tB, ownerAB[relB], "widened scope returns the B-owned doc labeled tenant B")
}
