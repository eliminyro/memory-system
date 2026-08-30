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

// TestDocumentDelete_PrunesCleanupQueue (F6): deleting a doc that is half of a
// pending cleanup pair removes the queue row (cleanup_queue has no FK cascade).
func TestDocumentDelete_PrunesCleanupQueue(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()
	ten := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, ten) })

	docA := seedDoc(t, db, ten, "a-"+uuid.NewString(), vec2D(1, 0))
	docB := seedDoc(t, db, ten, "b-"+uuid.NewString(), vec2D(0, 1))

	lo, hi := docA, docB
	if hi.String() < lo.String() {
		lo, hi = hi, lo
	}
	require.NoError(t, db.Create(&models.CleanupQueue{
		TenantID: ten, DocAID: lo, DocBID: hi, Similarity: 0.95,
	}).Error)

	pairRows := func() int64 {
		var n int64
		require.NoError(t, db.Model(&models.CleanupQueue{}).
			Where("doc_a_id = ? OR doc_b_id = ?", docA, docA).Count(&n).Error)
		return n
	}
	require.EqualValues(t, 1, pairRows(), "queue row exists before delete")

	require.NoError(t, repository.NewDocumentRepository(db).Delete(ctx, ten, docA))
	require.Zero(t, pairRows(), "deleting a doc must prune its cleanup_queue rows")
}

// TestGetRelated_NoEmbeddingTargetReturnsEmpty (F7): a target whose sections have
// no embedding must return no related docs, not arbitrary ones.
func TestGetRelated_NoEmbeddingTargetReturnsEmpty(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()
	ten := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, ten) })

	// Candidates with real embeddings a NULL-target would otherwise rank arbitrarily.
	seedDoc(t, db, ten, "cand1-"+uuid.NewString(), vec2D(1, 0))
	seedDoc(t, db, ten, "cand2-"+uuid.NewString(), vec2D(0, 1))

	target := seedDoc(t, db, ten, "target-"+uuid.NewString(), vec2D(1, 1))
	require.NoError(t, db.Exec("UPDATE sections SET embedding = NULL WHERE document_id = ?", target).Error)

	got, err := repository.NewSectionRepository(db).GetRelated(ctx, []uuid.UUID{ten}, target, 5)
	require.NoError(t, err)
	require.Empty(t, got, "no-embedding target must yield no related docs")
}
