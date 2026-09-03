package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
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
	// SubjectID is the key's unified subject id: its explicit subject_id when
	// set, else the tenant service principal "svc:<tenant_id>". Always user-type.
	SubjectID string
}

// keySubjectID resolves an API key's subject id: the explicit subject_id when
// non-empty, else the tenant service principal (see authz.ServicePrincipalID).
func keySubjectID(subjectID *string, tenantID uuid.UUID) string {
	if subjectID != nil && *subjectID != "" {
		return *subjectID
	}
	return authz.ServicePrincipalID(tenantID.String())
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
	// A key authenticates only when it is not revoked AND not past its expiry.
	// NULL expires_at means "never expires" (pre-expiry behavior preserved).
	if err := v.db.WithContext(ctx).
		Where("key_hash = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())", hash).
		First(&ak).Error; err != nil {
		return KeyInfo{}, fmt.Errorf("invalid or revoked API key")
	}
	// Best-effort last-used stamp for the admin listing — never fail auth on it.
	// Throttled to at most ~once/minute via a staleness predicate: without it,
	// every authenticated request (one hot key drives most traffic) turns a read
	// into a write + row-lock serialized on that single row. ~60s granularity is
	// plenty for an admin "last used" display.
	now := time.Now()
	v.db.WithContext(ctx).Model(&models.APIKey{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", ak.ID, now.Add(-time.Minute)).
		UpdateColumn("last_used_at", now)
	// API keys carry no user email: identity/email now lives on tenant_users, not
	// on the tenant, and resolving it here would add a read to the hot auth path.
	return KeyInfo{
		TenantID:  ak.TenantID,
		SubjectID: keySubjectID(ak.SubjectID, ak.TenantID),
	}, nil
}
