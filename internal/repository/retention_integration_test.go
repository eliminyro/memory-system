//go:build integration

package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// retDim matches the dimension the other Postgres integration tests migrate with,
// so a shared test DB migrated by one suite doesn't trip another's dimension guard.
const retDim = 768

func openRetentionPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", retDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func retTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "ret-" + uuid.NewString()}
	if err := db.Create(&ten).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return ten.ID
}

// retDoc inserts a document with an explicit archived_at (nil = live).
func retDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, docType, slug string, archivedAt *time.Time) uuid.UUID {
	t.Helper()
	doc := models.Document{
		ID:         uuid.New(),
		TenantID:   tenantID,
		Category:   "learnings",
		Slug:       slug,
		Title:      "T " + slug,
		DocType:    docType,
		ArchivedAt: archivedAt,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return doc.ID
}

// retSection inserts a section under docID and force-stamps its timestamps via raw
// SQL (gorm auto-fills created/updated on insert) for deterministic freshness values.
func retSection(t *testing.T, db *gorm.DB, docID uuid.UUID, created, updated time.Time, verified *time.Time) {
	t.Helper()
	sec := models.Section{
		ID:         uuid.New(),
		DocumentID: docID,
		Ordinal:    0,
		Content:    "body",
		Embedding:  pgvector.NewVector(make([]float32, retDim)),
	}
	if err := db.Create(&sec).Error; err != nil {
		t.Fatalf("create section: %v", err)
	}
	if err := db.Exec(
		`UPDATE sections SET created_at = ?, updated_at = ?, verified_at = ? WHERE id = ?`,
		created, updated, verified, sec.ID,
	).Error; err != nil {
		t.Fatalf("stamp section: %v", err)
	}
}

func docExists(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var c int64
	if err := db.Model(&models.Document{}).Where("id = ?", id).Count(&c).Error; err != nil {
		t.Fatalf("count doc: %v", err)
	}
	return c > 0
}

func docArchived(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var doc models.Document
	if err := db.Where("id = ?", id).First(&doc).Error; err != nil {
		t.Fatalf("load doc %s: %v", id, err)
	}
	return doc.ArchivedAt != nil
}

// TestRetentionDeleteArchived proves DeleteArchived hard-deletes only the target
// tenant's docs past the grace cutoff, writes one audit row per victim, purges
// cleanup_queue rows referencing a victim, and leaves within-grace/live/other tenants untouched.
func TestRetentionDeleteArchived(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	repo := repository.NewRetentionRepository(db)

	tenantA := retTenant(t, db)
	tenantB := retTenant(t, db)

	now := time.Now()
	pastGrace := now.Add(-40 * 24 * time.Hour)  // older than the cutoff -> deletable
	withinGrace := now.Add(-5 * 24 * time.Hour) // newer than the cutoff -> survives
	before := now.Add(-30 * 24 * time.Hour)     // the grace cutoff passed to DeleteArchived

	aPastSlug := "a-past-" + uuid.NewString()
	aPast := retDoc(t, db, tenantA, models.DocTypeLearning, aPastSlug, &pastGrace)
	aWithin := retDoc(t, db, tenantA, models.DocTypeLearning, "a-within-"+uuid.NewString(), &withinGrace)
	aLive := retDoc(t, db, tenantA, models.DocTypeLearning, "a-live-"+uuid.NewString(), nil)
	bPast := retDoc(t, db, tenantB, models.DocTypeLearning, "b-past-"+uuid.NewString(), &pastGrace)

	// A queue row referencing the victim must be purged; one referencing only
	// survivors must remain.
	victimQ := models.CleanupQueue{ID: uuid.New(), TenantID: tenantA, DocAID: aPast, DocBID: aLive, Similarity: 0.99, DetectedAt: now}
	survivorQ := models.CleanupQueue{ID: uuid.New(), TenantID: tenantA, DocAID: aWithin, DocBID: aLive, Similarity: 0.98, DetectedAt: now}
	if err := db.Create(&victimQ).Error; err != nil {
		t.Fatalf("create victim queue row: %v", err)
	}
	if err := db.Create(&survivorQ).Error; err != nil {
		t.Fatalf("create survivor queue row: %v", err)
	}

	n, err := repo.DeleteArchived(ctx, tenantA, before)
	if err != nil {
		t.Fatalf("DeleteArchived: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteArchived rows = %d, want 1 (only A's past-grace doc)", n)
	}

	// Only the past-grace doc for tenant A is gone.
	if docExists(t, db, aPast) {
		t.Error("A's past-grace doc should have been deleted")
	}
	if !docExists(t, db, aWithin) {
		t.Error("A's within-grace doc must survive")
	}
	if !docExists(t, db, aLive) {
		t.Error("A's unarchived doc must survive")
	}
	if !docExists(t, db, bPast) {
		t.Error("B's past-grace doc must survive (DeleteArchived was scoped to A)")
	}

	// Exactly one audit row, for tenant A, describing the victim.
	var events []models.DeletionEvent
	if err := db.Where("tenant_id = ?", tenantA).Find(&events).Error; err != nil {
		t.Fatalf("load deletion events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("deletion_events for A = %d, want 1", len(events))
	}
	if got, want := events[0].DocumentPath, "learnings/"+aPastSlug; got != want {
		t.Errorf("deletion event path = %q, want %q", got, want)
	}
	if events[0].Reason != models.DeletionReasonRetention {
		t.Errorf("deletion event reason = %q, want %q", events[0].Reason, models.DeletionReasonRetention)
	}
	// No audit row leaked into tenant B.
	var bEvents int64
	if err := db.Model(&models.DeletionEvent{}).Where("tenant_id = ?", tenantB).Count(&bEvents).Error; err != nil {
		t.Fatalf("count B events: %v", err)
	}
	if bEvents != 0 {
		t.Errorf("deletion_events for B = %d, want 0", bEvents)
	}

	// Queue row referencing the victim purged; survivor-only row untouched.
	var victimCount, survivorCount int64
	db.Model(&models.CleanupQueue{}).Where("id = ?", victimQ.ID).Count(&victimCount)
	db.Model(&models.CleanupQueue{}).Where("id = ?", survivorQ.ID).Count(&survivorCount)
	if victimCount != 0 {
		t.Error("cleanup_queue row referencing a victim should have been purged")
	}
	if survivorCount != 1 {
		t.Error("cleanup_queue row referencing only survivors must remain")
	}
}

// TestRetentionArchiveExpired proves ArchiveExpired archives only the target tenant's
// docs whose freshest section predates the per-doc_type cutoff, keeps recently-touched
// docs alive (an edit bumping updated_at counts, not just mark_verified), and ignores
// absent doc_types and other tenants.
func TestRetentionArchiveExpired(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	repo := repository.NewRetentionRepository(db)

	tenantA := retTenant(t, db)
	tenantB := retTenant(t, db)

	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	cutoff := now.Add(-24 * time.Hour) // a doc stays alive iff a section is >= this

	// Stale: freshest section is old -> should be archived.
	staleDoc := retDoc(t, db, tenantA, models.DocTypeLearning, "stale-"+uuid.NewString(), nil)
	retSection(t, db, staleDoc, old, old, nil)

	// Fresh: updated_at is recent (verified_at still nil) -> must stay alive.
	freshDoc := retDoc(t, db, tenantA, models.DocTypeLearning, "fresh-"+uuid.NewString(), nil)
	retSection(t, db, freshDoc, old, now, nil)

	// Other tenant, stale section -> untouched (ArchiveExpired scoped to A).
	otherDoc := retDoc(t, db, tenantB, models.DocTypeLearning, "other-"+uuid.NewString(), nil)
	retSection(t, db, otherDoc, old, old, nil)

	// A doc_type absent from the cutoff map -> untouched even though it's stale.
	prefDoc := retDoc(t, db, tenantA, models.DocTypePreference, "pref-"+uuid.NewString(), nil)
	retSection(t, db, prefDoc, old, old, nil)

	cutoffs := map[string]time.Time{models.DocTypeLearning: cutoff}
	n, err := repo.ArchiveExpired(ctx, tenantA, cutoffs)
	if err != nil {
		t.Fatalf("ArchiveExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("ArchiveExpired rows = %d, want 1 (only the stale learning doc)", n)
	}

	if !docArchived(t, db, staleDoc) {
		t.Error("stale doc should have been archived")
	}
	if docArchived(t, db, freshDoc) {
		t.Error("recently-touched doc must stay alive")
	}
	if docArchived(t, db, otherDoc) {
		t.Error("other tenant's doc must be untouched")
	}
	if docArchived(t, db, prefDoc) {
		t.Error("doc_type absent from the cutoff map must be untouched")
	}
}
