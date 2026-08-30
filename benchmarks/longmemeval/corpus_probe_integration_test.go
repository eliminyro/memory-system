//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

const probeTestDim = 768

func openProbePG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", probeTestDim, database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func createProbeTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	tenantID := uuid.New()
	tenant := &models.Tenant{ID: tenantID, Name: "probe-test-" + tenantID.String(), Type: models.TenantTypeShared}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return tenantID
}

// TestVerifyCorpusPresence_MissingCorpusFailsLoudly locks task 4.2: an absent
// corpus (never ingested, or a different dataset/seed/n) must error rather
// than let --skip-ingest silently score to zero recall.
func TestVerifyCorpusPresence_MissingCorpusFailsLoudly(t *testing.T) {
	db := openProbePG(t)
	tenantID := createProbeTenant(t, db)

	slice := []Instance{{QuestionID: "q1"}, {QuestionID: "q2"}}
	if err := VerifyCorpusPresence(context.Background(), db, tenantID, slice); err == nil {
		t.Fatal("expected an error for a missing corpus, got nil")
	}
}

func TestVerifyCorpusPresence_PresentCorpusPasses(t *testing.T) {
	db := openProbePG(t)
	tenantID := createProbeTenant(t, db)

	slice := []Instance{{QuestionID: "q1"}, {QuestionID: "q2"}}
	for _, inst := range slice {
		subcat := CanonicalSegment(inst.QuestionID)
		doc := &models.Document{
			TenantID:    tenantID,
			Category:    benchCategory,
			Subcategory: &subcat,
			Slug:        "sess-1",
			Title:       "t",
		}
		if err := db.Create(doc).Error; err != nil {
			t.Fatalf("create document: %v", err)
		}
	}

	if err := VerifyCorpusPresence(context.Background(), db, tenantID, slice); err != nil {
		t.Fatalf("expected no error for a present corpus, got %v", err)
	}
}

// TestVerifyCorpusPresence_PartialCorpusFails covers a slice-mismatch case
// (e.g. a smaller --n on the prior ingest): some but not all expected
// subcategories present must still fail loudly, not partially score.
func TestVerifyCorpusPresence_PartialCorpusFails(t *testing.T) {
	db := openProbePG(t)
	tenantID := createProbeTenant(t, db)

	subcat := CanonicalSegment("q1")
	doc := &models.Document{
		TenantID:    tenantID,
		Category:    benchCategory,
		Subcategory: &subcat,
		Slug:        "sess-1",
		Title:       "t",
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("create document: %v", err)
	}

	slice := []Instance{{QuestionID: "q1"}, {QuestionID: "q2"}}
	if err := VerifyCorpusPresence(context.Background(), db, tenantID, slice); err == nil {
		t.Fatal("expected an error for a partially-ingested corpus, got nil")
	}
}
