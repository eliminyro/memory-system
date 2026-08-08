//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestRetentionDeleteArchived_SingleCallerNoDoubleAudit proves the F5 advisory-lock
// wrap didn't break normal single-caller operation: one DeleteArchived deletes the
// archived victim and writes exactly ONE deletion_events row for it (the transaction
// + advisory lock do not double-write the audit in a single-caller run). The
// cross-replica dedup guarantee is a property of the tx-scoped advisory lock and is
// deliberately not exercised with a flaky two-goroutine test.
func TestRetentionDeleteArchived_SingleCallerNoDoubleAudit(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	repo := repository.NewRetentionRepository(db)

	tenant := retTenant(t, db)
	now := time.Now()
	before := now.Add(-30 * 24 * time.Hour)    // the grace cutoff
	pastGrace := now.Add(-40 * 24 * time.Hour) // archived past the cutoff -> deletable

	slug := "lock-" + uuid.NewString()
	victim := retDoc(t, db, tenant, models.DocTypeLearning, slug, &pastGrace)

	n, err := repo.DeleteArchived(ctx, tenant, before)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	require.False(t, docExists(t, db, victim), "archived past-grace doc must be deleted")

	var events []models.DeletionEvent
	require.NoError(t, db.Where("tenant_id = ?", tenant).Find(&events).Error)
	require.Len(t, events, 1, "exactly one deletion_events row per victim (no duplicate audit)")
	require.Equal(t, "learnings/"+slug, events[0].DocumentPath)
	require.Equal(t, models.DeletionReasonRetention, events[0].Reason)
}
