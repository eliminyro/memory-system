//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestCleanupQueue_DismissedPairNotReEnqueued guards audit #19: once a pair is
// resolved as false_positive/ignored, a later scan that surfaces the same pair
// must not re-add it to the pending queue.
func TestCleanupQueue_DismissedPairNotReEnqueued(t *testing.T) {
	db := openLintPG(t)
	tenantID := seedTenant(t, db)
	t.Cleanup(func() {
		db.Exec("DELETE FROM cleanup_queue WHERE tenant_id = ?", tenantID)
		cleanupTenant(db, tenantID)
	})

	emb := make([]float32, dupTestDim)
	emb[0] = 1
	a := seedDoc(t, db, tenantID, "dup-a", emb)
	b := seedDoc(t, db, tenantID, "dup-b", emb)

	repo := repository.NewCleanupQueueRepository(db)
	ctx := context.Background()

	// First scan enqueues the pair.
	inserted, err := repo.Upsert(ctx, &models.CleanupQueue{TenantID: tenantID, DocAID: a, DocBID: b, Similarity: 0.99})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !inserted {
		t.Fatalf("first upsert did not insert the pair")
	}

	// Operator dismisses it as a false positive.
	pending, err := repo.ListPending(ctx, tenantID, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending entry, got %d", len(pending))
	}
	if err := repo.Resolve(ctx, tenantID, pending[0].ID, models.CleanupResolutionFalsePositive, "not a duplicate", nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The next nightly scan surfaces the same pair — it must NOT be re-added.
	inserted, err = repo.Upsert(ctx, &models.CleanupQueue{TenantID: tenantID, DocAID: a, DocBID: b, Similarity: 0.995})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if inserted {
		t.Fatalf("dismissed pair was re-enqueued on a subsequent scan")
	}

	pendingAfter, err := repo.ListPending(ctx, tenantID, 10)
	if err != nil {
		t.Fatalf("list pending after dismissal: %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Fatalf("want 0 pending after dismissal, got %d", len(pendingAfter))
	}

	// A pair the operator ignored is likewise suppressed.
	c := seedDoc(t, db, tenantID, "dup-c", emb)
	d := seedDoc(t, db, tenantID, "dup-d", emb)
	if _, err := repo.Upsert(ctx, &models.CleanupQueue{TenantID: tenantID, DocAID: c, DocBID: d, Similarity: 0.99}); err != nil {
		t.Fatalf("upsert c/d: %v", err)
	}
	pend, err := repo.ListPending(ctx, tenantID, 10)
	if err != nil {
		t.Fatalf("list pending c/d: %v", err)
	}
	cdID := pend[0].ID
	if err := repo.Resolve(ctx, tenantID, cdID, models.CleanupResolutionIgnored, "ignore", nil); err != nil {
		t.Fatalf("resolve ignored: %v", err)
	}
	insertedCD, err := repo.Upsert(ctx, &models.CleanupQueue{TenantID: tenantID, DocAID: c, DocBID: d, Similarity: 0.99})
	if err != nil {
		t.Fatalf("re-upsert c/d: %v", err)
	}
	if insertedCD {
		t.Fatalf("ignored pair was re-enqueued")
	}
}
