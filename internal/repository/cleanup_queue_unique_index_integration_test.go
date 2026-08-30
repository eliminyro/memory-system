//go:build integration

package repository_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestCleanupQueue_PartialUniqueIndex guards B10: the migration creates a partial
// unique index on (tenant_id, doc_a_id, doc_b_id) WHERE resolved_at IS NULL, so two
// replicas racing the check-then-insert Upsert can't both insert the same pending
// pair. The partial predicate still allows re-enqueue once a pair is resolved.
func TestCleanupQueue_PartialUniqueIndex(t *testing.T) {
	db := openLintPG(t)
	tenantID := seedTenant(t, db)
	t.Cleanup(func() {
		db.Exec("DELETE FROM cleanup_queue WHERE tenant_id = ?", tenantID)
		cleanupTenant(db, tenantID)
	})

	var idx int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_cleanup_pending_pair'`).Scan(&idx).Error)
	require.Equal(t, int64(1), idx, "partial unique index idx_cleanup_pending_pair is missing")

	a, b := uuid.New(), uuid.New()
	first := &models.CleanupQueue{TenantID: tenantID, DocAID: a, DocBID: b, Similarity: 0.9}
	require.NoError(t, db.Create(first).Error)

	// A concurrent peer's duplicate PENDING insert must violate the index.
	dup := &models.CleanupQueue{TenantID: tenantID, DocAID: a, DocBID: b, Similarity: 0.9}
	require.Error(t, db.Create(dup).Error, "duplicate pending pair should violate the partial unique index")

	// Once resolved, the partial predicate no longer covers the row, so the pair can
	// legitimately re-enqueue if it re-forms.
	require.NoError(t, db.Model(&models.CleanupQueue{}).Where("id = ?", first.ID).
		Update("resolved_at", gorm.Expr("NOW()")).Error)
	reopened := &models.CleanupQueue{TenantID: tenantID, DocAID: a, DocBID: b, Similarity: 0.95}
	require.NoError(t, db.Create(reopened).Error, "resolved pair must be re-enqueuable")
}
