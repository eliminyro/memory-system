//go:build integration

package cleanup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

const scanDim = 768

func openScannerPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", scanDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// scanTenant creates a non-bootstrap tenant with staleness OFF, so access-recency
// eviction is exercised purely through the global toggle (not staleness).
func scanTenant(t *testing.T, db *gorm.DB) models.Tenant {
	t.Helper()
	ten := models.Tenant{
		ID:            uuid.New(),
		Name:          "scan-" + uuid.NewString(),
		StalenessMode: models.StalenessModeOff, // access eviction is independent of staleness
	}
	if err := db.Create(&ten).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return ten
}

func scanDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, lastAccessed time.Time, slug string) uuid.UUID {
	t.Helper()
	doc := models.Document{
		ID:       uuid.New(),
		TenantID: tenantID,
		Category: "learnings",
		Slug:     slug,
		Title:    "T " + slug,
		DocType:  models.DocTypeLearning,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := db.Exec(`UPDATE documents SET last_accessed_at = ? WHERE id = ?`, lastAccessed, doc.ID).Error; err != nil {
		t.Fatalf("stamp doc: %v", err)
	}
	return doc.ID
}

func scanDocExists(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var c int64
	if err := db.Model(&models.Document{}).Where("id = ?", id).Count(&c).Error; err != nil {
		t.Fatalf("count doc: %v", err)
	}
	return c > 0
}

func scanDocArchived(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var doc models.Document
	if err := db.Where("id = ?", id).First(&doc).Error; err != nil {
		t.Fatalf("load doc %s: %v", id, err)
	}
	return doc.ArchivedAt != nil
}

// TestRetainTenant_AccessEviction proves retainTenant wires access-recency
// eviction to the GLOBAL toggle (not a per-tenant setting) and per-category
// windows (threshold × multiplier), independent of staleness_mode: with the
// toggle on, an eligible tenant archives cold unpinned docs and, past grace,
// DeleteArchived hard-deletes them; a warm doc survives; with the toggle off no
// tenant is touched.
func TestRetainTenant_AccessEviction(t *testing.T) {
	db := openScannerPG(t)
	ctx := context.Background()
	// multiplier=3, graceDays=30. learning threshold=180 ⇒ access window 540 days.
	s := &Scanner{
		retention:  repository.NewRetentionRepository(db),
		thresholds: staleness.NewThresholdStore(db),
		multiplier: 3,
		graceDays:  30,
	}

	now := time.Now()
	cold := now.Add(-600 * 24 * time.Hour) // past the 540-day learning access window
	warm := now.Add(-5 * 24 * time.Hour)

	tenantA := scanTenant(t, db)
	tenantB := scanTenant(t, db)

	coldDoc := scanDoc(t, db, tenantA.ID, cold, "cold-"+uuid.NewString())
	warmDoc := scanDoc(t, db, tenantA.ID, warm, "warm-"+uuid.NewString())
	otherCold := scanDoc(t, db, tenantB.ID, cold, "other-"+uuid.NewString())

	// Toggle OFF: nothing is access-evicted anywhere (staleness off ⇒ untouched).
	if err := s.retainTenant(ctx, tenantB, false, &ScanStats{}); err != nil {
		t.Fatalf("retainTenant(toggle off): %v", err)
	}
	if scanDocArchived(t, db, otherCold) || !scanDocExists(t, db, otherCold) {
		t.Error("toggle-off tenant's cold doc must be untouched")
	}

	// Toggle ON: cold doc archived (not yet deleted — archived_at=now within grace).
	stats := &ScanStats{}
	if err := s.retainTenant(ctx, tenantA, true, stats); err != nil {
		t.Fatalf("retainTenant(toggle on): %v", err)
	}
	if !scanDocArchived(t, db, coldDoc) {
		t.Error("eligible tenant's cold doc should have been archived")
	}
	if scanDocArchived(t, db, warmDoc) {
		t.Error("warm doc must survive")
	}
	if stats.DocsArchived != 1 {
		t.Errorf("stats.DocsArchived = %d, want 1", stats.DocsArchived)
	}

	// Backdate the access-archived doc past grace; the next sweep's DeleteArchived
	// must hard-delete it (access-archived docs roll off after grace too).
	if err := db.Exec(`UPDATE documents SET archived_at = ? WHERE id = ?`, cold, coldDoc).Error; err != nil {
		t.Fatalf("backdate archived_at: %v", err)
	}
	stats2 := &ScanStats{}
	if err := s.retainTenant(ctx, tenantA, true, stats2); err != nil {
		t.Fatalf("retainTenant(toggle on) sweep 2: %v", err)
	}
	if scanDocExists(t, db, coldDoc) {
		t.Error("access-archived doc past grace should have been hard-deleted")
	}
	if stats2.DocsDeleted < 1 {
		t.Errorf("stats.DocsDeleted = %d, want >= 1", stats2.DocsDeleted)
	}
	if !scanDocExists(t, db, warmDoc) {
		t.Error("warm doc must survive both sweeps")
	}
}
