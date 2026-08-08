//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	apperr "github.com/eliminyro/memory-system/internal/errors"
)

const apiKeyTestDim = 768

func openAPIKeyPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", apiKeyTestDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestAPIKeyGetByID_NotFound locks the not-found mapping (L4): a missing id
// yields apperr.ErrNotFound. After the fix, GetByID only maps
// gorm.ErrRecordNotFound to ErrNotFound — any genuine DB error now propagates
// verbatim instead of being masked as not-found, so this test guards the one
// case that must still surface as ErrNotFound.
func TestAPIKeyGetByID_NotFound(t *testing.T) {
	db := openAPIKeyPG(t)
	repo := NewAPIKeyRepository(db)

	_, err := repo.GetByID(context.Background(), uuid.New())
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("GetByID(random uuid) err = %v, want apperr.ErrNotFound", err)
	}
}
