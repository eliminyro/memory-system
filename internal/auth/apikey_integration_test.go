//go:build integration

package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

const apiKeyTestDim = 768

func openAuthPG(t *testing.T) *gorm.DB {
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

// TestValidateKeyRealDB exercises the real DB path: a live key resolves to the
// right tenant + service-principal subject; unknown hash and revoked key error.
func TestValidateKeyRealDB(t *testing.T) {
	db := openAuthPG(t)
	ctx := context.Background()
	v := NewAPIKeyValidator(db)

	tenant := models.Tenant{ID: uuid.New(), Name: "apikey-" + uuid.NewString(), Email: "owner@example.com"}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key := models.APIKey{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		KeyHash:  hash,
		Label:    "test-key",
		Prefix:   KeyPrefix(plaintext),
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// Valid key: resolves to owning tenant + service-principal subject + email.
	info, err := v.ValidateKey(ctx, plaintext)
	if err != nil {
		t.Fatalf("ValidateKey(valid) error: %v", err)
	}
	if info.TenantID != tenant.ID {
		t.Errorf("TenantID = %s, want %s", info.TenantID, tenant.ID)
	}
	if want := authz.ServicePrincipalID(tenant.ID.String()); info.SubjectID != want {
		t.Errorf("SubjectID = %q, want %q", info.SubjectID, want)
	}
	if info.Email != tenant.Email {
		t.Errorf("Email = %q, want %q", info.Email, tenant.Email)
	}

	// Unknown hash: no matching row -> error.
	if _, err := v.ValidateKey(ctx, "mmcp_this-key-was-never-issued"); err == nil {
		t.Error("ValidateKey(unknown) = nil error, want error")
	}

	// Revoke it, then the same plaintext must fail (revoked_at IS NULL filter).
	if err := db.Model(&models.APIKey{}).Where("id = ?", key.ID).
		Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	if _, err := v.ValidateKey(ctx, plaintext); err == nil {
		t.Error("ValidateKey(revoked) = nil error, want error")
	}
}

// TestValidateKeyPinnedSubject proves a key carrying an explicit subject_id
// resolves to that subject rather than the tenant service principal.
func TestValidateKeyPinnedSubject(t *testing.T) {
	db := openAuthPG(t)
	ctx := context.Background()
	v := NewAPIKeyValidator(db)

	tenant := models.Tenant{ID: uuid.New(), Name: "apikey-pinned-" + uuid.NewString()}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	subject := "user:alice-" + uuid.NewString()
	key := models.APIKey{
		ID:        uuid.New(),
		TenantID:  tenant.ID,
		KeyHash:   hash,
		Label:     "pinned-key",
		Prefix:    KeyPrefix(plaintext),
		SubjectID: &subject,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}

	info, err := v.ValidateKey(ctx, plaintext)
	if err != nil {
		t.Fatalf("ValidateKey(pinned) error: %v", err)
	}
	if info.SubjectID != subject {
		t.Errorf("SubjectID = %q, want pinned %q", info.SubjectID, subject)
	}
	if info.TenantID != tenant.ID {
		t.Errorf("TenantID = %s, want %s", info.TenantID, tenant.ID)
	}
}
