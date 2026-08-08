//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// stampDocTimestamps force-sets a document's created_at/updated_at (gorm
// auto-fills them on insert) so the zero-section fallback can be exercised
// deterministically against a fixed cutoff.
func stampDocTimestamps(t *testing.T, db *gorm.DB, id uuid.UUID, ts time.Time) {
	t.Helper()
	if err := db.Exec(
		`UPDATE documents SET created_at = ?, updated_at = ? WHERE id = ?`,
		ts, ts, id,
	).Error; err != nil {
		t.Fatalf("stamp doc: %v", err)
	}
}

// TestArchiveExpired_ZeroSectionFallback is the L3 regression: a document with
// ZERO sections satisfies the "no fresh section" NOT EXISTS trivially and, before
// the fix, was archived on its very next sweep regardless of its own age. The
// added guard falls back to the document's own created_at/updated_at for the
// zero-section case, without changing behavior for docs that have sections.
func TestArchiveExpired_ZeroSectionFallback(t *testing.T) {
	db := openRetentionPG(t)
	repo := repository.NewRetentionRepository(db)
	ctx := context.Background()
	tenant := retTenant(t, db)

	// Zero-section doc whose own timestamps are AFTER the cutoff -> must survive.
	zeroDoc := retDoc(t, db, tenant, models.DocTypeLearning, "zero-"+uuid.NewString(), nil)
	future := time.Now().Add(time.Hour)
	stampDocTimestamps(t, db, zeroDoc, future)

	// Doc WITH a fresh section (timestamps in the future) -> must survive
	// (original behavior, unaffected by the fallback).
	withSecDoc := retDoc(t, db, tenant, models.DocTypeLearning, "withsec-"+uuid.NewString(), nil)
	retSection(t, db, withSecDoc, future, future, &future)

	cutoff := time.Now()
	n, err := repo.ArchiveExpired(ctx, tenant, map[string]time.Time{models.DocTypeLearning: cutoff})
	if err != nil {
		t.Fatalf("ArchiveExpired (fresh phase): %v", err)
	}
	if n != 0 {
		t.Fatalf("ArchiveExpired rows = %d, want 0 (nothing older than cutoff yet)", n)
	}
	if docArchived(t, db, zeroDoc) {
		t.Error("freshly-created zero-section doc must NOT be archived")
	}
	if docArchived(t, db, withSecDoc) {
		t.Error("doc with a fresh section must NOT be archived")
	}

	// Backdate the zero-section doc's own timestamps to before the cutoff -> now
	// it IS eligible via the fallback.
	past := time.Now().Add(-time.Hour)
	stampDocTimestamps(t, db, zeroDoc, past)

	cutoff2 := time.Now()
	n2, err := repo.ArchiveExpired(ctx, tenant, map[string]time.Time{models.DocTypeLearning: cutoff2})
	if err != nil {
		t.Fatalf("ArchiveExpired (aged phase): %v", err)
	}
	if n2 != 1 {
		t.Fatalf("ArchiveExpired rows = %d, want 1 (only the aged zero-section doc)", n2)
	}
	if !docArchived(t, db, zeroDoc) {
		t.Error("aged zero-section doc should now be archived")
	}
	if docArchived(t, db, withSecDoc) {
		t.Error("doc with a fresh section must remain alive across both sweeps")
	}
}
