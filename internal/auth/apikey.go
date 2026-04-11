package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

const apiKeyPrefix = "mmcp_"

// GenerateAPIKey creates a new random API key.
// Returns the plaintext key (to show once) and its SHA-256 hash (to store).
func GenerateAPIKey() (plaintext, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate random bytes: %w", err)
	}
	plaintext = apiKeyPrefix + hex.EncodeToString(b)
	hash = HashAPIKey(plaintext)
	return plaintext, hash, nil
}

// HashAPIKey returns the SHA-256 hex digest of a key.
func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// KeyPrefix returns the display prefix for an API key (first 8 chars after mmcp_).
func KeyPrefix(plaintext string) string {
	withoutPrefix := plaintext[len(apiKeyPrefix):]
	if len(withoutPrefix) > 8 {
		return withoutPrefix[:8]
	}
	return withoutPrefix
}

// KeyInfo holds the resolved identity from an API key lookup.
type KeyInfo struct {
	TenantID uuid.UUID
	Email    string
}

// APIKeyValidator looks up API keys and resolves them to tenant IDs.
type APIKeyValidator struct {
	db *gorm.DB
}

func NewAPIKeyValidator(db *gorm.DB) *APIKeyValidator {
	return &APIKeyValidator{db: db}
}

// ValidateKey checks the key hash against the database.
// Returns the tenant info if valid, or an error if not found / revoked.
func (v *APIKeyValidator) ValidateKey(ctx context.Context, key string) (KeyInfo, error) {
	hash := HashAPIKey(key)
	var ak models.APIKey
	if err := v.db.WithContext(ctx).
		Preload("Tenant").
		Where("key_hash = ? AND revoked_at IS NULL", hash).
		First(&ak).Error; err != nil {
		return KeyInfo{}, fmt.Errorf("invalid or revoked API key")
	}
	var email string
	if ak.Tenant != nil {
		email = ak.Tenant.Email
	}
	return KeyInfo{TenantID: ak.TenantID, Email: email}, nil
}
