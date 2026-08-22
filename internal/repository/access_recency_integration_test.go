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

// accessDoc inserts a document with explicit last_accessed_at (nil = NULL),
// created_at, pinned, and archived_at (nil = live) for access-recency tests.
func accessDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, docType, slug string, lastAccessed *time.Time, createdAt time.Time, pinned bool, archivedAt *time.Time) uuid.UUID {
	t.Helper()
	doc := models.Document{
		ID:         uuid.New(),
		TenantID:   tenantID,
		Category:   "learnings",
		Slug:       slug,
		Title:      "T " + slug,
		DocType:    docType,
		Pinned:     pinned,
		ArchivedAt: archivedAt,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := db.Exec(
		`UPDATE documents SET last_accessed_at = ?, created_at = ? WHERE id = ?`,
		lastAccessed, createdAt, doc.ID,
	).Error; err != nil {
		t.Fatalf("stamp doc: %v", err)
	}
	return doc.ID
}

func lastAccessedAt(t *testing.T, db *gorm.DB, id uuid.UUID) *time.Time {
	t.Helper()
	var doc models.Document
	if err := db.Where("id = ?", id).First(&doc).Error; err != nil {
		t.Fatalf("load doc %s: %v", id, err)
	}
	return doc.LastAccessedAt
}

// TestTouchAccessed proves the day-granular serve-bump: a NULL or stale
// last_accessed_at advances to today, a doc already touched today is not
// re-written (<=1 write/day), unlisted docs are untouched, and empty input no-ops.
func TestTouchAccessed(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	repo := repository.NewDocumentRepository(db)

	tenant := retTenant(t, db)
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	earlierToday := now.Add(-1 * time.Hour) // still >= date_trunc('day', now())
	todayStart := now.Truncate(24 * time.Hour)

	docNull := accessDoc(t, db, tenant, models.DocTypeLearning, "null-"+uuid.NewString(), nil, now, false, nil)
	docYesterday := accessDoc(t, db, tenant, models.DocTypeLearning, "yd-"+uuid.NewString(), &yesterday, now, false, nil)
	docToday := accessDoc(t, db, tenant, models.DocTypeLearning, "today-"+uuid.NewString(), &earlierToday, now, false, nil)
	docUnlisted := accessDoc(t, db, tenant, models.DocTypeLearning, "unlisted-"+uuid.NewString(), nil, now, false, nil)

	// Empty input is a no-op returning nil.
	if err := repo.TouchAccessed(ctx, nil); err != nil {
		t.Fatalf("TouchAccessed(nil): %v", err)
	}

	if err := repo.TouchAccessed(ctx, []uuid.UUID{docNull, docYesterday, docToday}); err != nil {
		t.Fatalf("TouchAccessed: %v", err)
	}

	// NULL -> advanced to today.
	if la := lastAccessedAt(t, db, docNull); la == nil || la.Before(todayStart) {
		t.Errorf("null-doc last_accessed_at = %v, want >= today start", la)
	}
	// Yesterday -> advanced past yesterday.
	if la := lastAccessedAt(t, db, docYesterday); la == nil || !la.After(yesterday) {
		t.Errorf("yesterday-doc last_accessed_at = %v, want advanced past %v", la, yesterday)
	}
	// Already today -> guard skips the write, value unchanged.
	if la := lastAccessedAt(t, db, docToday); la == nil || !la.Truncate(time.Second).Equal(earlierToday.Truncate(time.Second)) {
		t.Errorf("today-doc last_accessed_at = %v, want unchanged %v (<=1 write/day)", la, earlierToday)
	}
	// Unlisted -> untouched, stays NULL.
	if la := lastAccessedAt(t, db, docUnlisted); la != nil {
		t.Errorf("unlisted-doc last_accessed_at = %v, want nil (untouched)", la)
	}
}

// TestArchiveAccessCold proves the PER-DOC_TYPE eviction predicate: only the
// target tenant's unpinned, non-episodic, unarchived docs whose
// COALESCE(last_accessed_at, created_at) predates THEIR doc_type's cutoff are
// archived (never hard-deleted here). A short-window doc_type goes cold before a
// long-window one at the same age (per-category windows).
func TestArchiveAccessCold(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	repo := repository.NewRetentionRepository(db)

	tenantA := retTenant(t, db)
	tenantB := retTenant(t, db)

	now := time.Now()
	cold := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-5 * 24 * time.Hour)
	// Per-category cutoffs: learning window short (30d), preference window long (100d).
	cutoffs := map[string]time.Time{
		models.DocTypeLearning:   now.Add(-30 * 24 * time.Hour),
		models.DocTypePreference: now.Add(-100 * 24 * time.Hour),
	}

	// Cold by last_accessed_at -> archived.
	coldAccessed := accessDoc(t, db, tenantA, models.DocTypeLearning, "cold-acc-"+uuid.NewString(), &cold, now, false, nil)
	// Never accessed, created long ago -> COALESCE falls to created_at, cold -> archived.
	coldCreated := accessDoc(t, db, tenantA, models.DocTypeLearning, "cold-cre-"+uuid.NewString(), nil, cold, false, nil)
	// Recently accessed old doc -> survives.
	recentlyAccessed := accessDoc(t, db, tenantA, models.DocTypeLearning, "warm-acc-"+uuid.NewString(), &recent, cold, false, nil)
	// Recently created, never accessed -> survives (creation-time grace).
	recentlyCreated := accessDoc(t, db, tenantA, models.DocTypeLearning, "warm-cre-"+uuid.NewString(), nil, recent, false, nil)
	// Pinned cold doc -> survives.
	pinnedCold := accessDoc(t, db, tenantA, models.DocTypeLearning, "pin-"+uuid.NewString(), &cold, now, true, nil)
	// Episodic cold doc -> survives (excluded from access eviction).
	episodicCold := accessDoc(t, db, tenantA, models.DocTypeJournal, "jrn-"+uuid.NewString(), &cold, now, false, nil)
	// Same age as the cold learning docs but a preference (longer window) -> survives.
	prefSameAge := accessDoc(t, db, tenantA, models.DocTypePreference, "pref-"+uuid.NewString(), &cold, now, false, nil)
	// Other tenant's cold doc -> untouched (scope).
	otherCold := accessDoc(t, db, tenantB, models.DocTypeLearning, "other-"+uuid.NewString(), &cold, now, false, nil)

	n, err := repo.ArchiveAccessCold(ctx, tenantA, cutoffs)
	if err != nil {
		t.Fatalf("ArchiveAccessCold: %v", err)
	}
	if n != 2 {
		t.Fatalf("ArchiveAccessCold rows = %d, want 2 (cold-accessed + cold-created)", n)
	}

	if !docArchived(t, db, coldAccessed) {
		t.Error("cold-by-access doc should have been archived")
	}
	if !docArchived(t, db, coldCreated) {
		t.Error("cold-by-creation (never accessed) doc should have been archived")
	}
	if docArchived(t, db, recentlyAccessed) {
		t.Error("recently-accessed doc must survive")
	}
	if docArchived(t, db, recentlyCreated) {
		t.Error("recently-created never-accessed doc must survive")
	}
	if docArchived(t, db, pinnedCold) {
		t.Error("pinned cold doc must survive")
	}
	if docArchived(t, db, episodicCold) {
		t.Error("episodic cold doc must survive (excluded)")
	}
	if docArchived(t, db, prefSameAge) {
		t.Error("preference doc at the same age must survive (longer per-category window)")
	}
	if docArchived(t, db, otherCold) {
		t.Error("other tenant's cold doc must be untouched")
	}
	// Archive-only: nothing was hard-deleted.
	if !docExists(t, db, coldAccessed) || !docExists(t, db, coldCreated) {
		t.Error("ArchiveAccessCold must not hard-delete (DeleteArchived does, after grace)")
	}
}
