//go:build integration

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestValidateKeyExpiry proves the expiry gate: past expires_at is rejected,
// future expiry authenticates, NULL never expires.
func TestValidateKeyExpiry(t *testing.T) {
	db := openAuthPG(t)
	ctx := context.Background()
	v := NewAPIKeyValidator(db)

	tenant := models.Tenant{ID: uuid.New(), Name: "apikey-exp-" + uuid.NewString()}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	mk := func(t *testing.T, exp *time.Time) string {
		t.Helper()
		plaintext, hash, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if err := db.Create(&models.APIKey{
			ID: uuid.New(), TenantID: tenant.ID, KeyHash: hash,
			Label: "exp", Prefix: KeyPrefix(plaintext), ExpiresAt: exp,
		}).Error; err != nil {
			t.Fatalf("create key: %v", err)
		}
		return plaintext
	}

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	expired := mk(t, &past)
	if _, err := v.ValidateKey(ctx, expired); err == nil {
		t.Error("expired key authenticated; want rejection")
	}

	live := mk(t, &future)
	if _, err := v.ValidateKey(ctx, live); err != nil {
		t.Errorf("future-expiry key rejected: %v", err)
	}

	never := mk(t, nil)
	if _, err := v.ValidateKey(ctx, never); err != nil {
		t.Errorf("non-expiring key rejected: %v", err)
	}
}
