package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type APIKeyRepository struct {
	db *gorm.DB
}

func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

func (r *APIKeyRepository) Create(ctx context.Context, key *models.APIKey) error {
	return r.db.WithContext(ctx).Create(key).Error
}

func (r *APIKeyRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]models.APIKey, error) {
	var keys []models.APIKey
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// GetByID returns a single key by id (including revoked/expired rows — callers
// like rotation need the metadata regardless of current validity).
func (r *APIKeyRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&key).Error; err != nil {
		return nil, fmt.Errorf("%w: api key %s", apperr.ErrNotFound, id)
	}
	return &key, nil
}

// SetExpiry sets expires_at on a key (used by rotation's grace window to let a
// predecessor stay valid until it lapses). A nil t clears any expiry.
func (r *APIKeyRepository) SetExpiry(ctx context.Context, id uuid.UUID, t *time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("expires_at", t)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: api key %s (not found or already revoked)", apperr.ErrNotFound, id)
	}
	return nil
}

// Revoke sets revoked_at on the key, effectively disabling it.
func (r *APIKeyRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: api key %s (not found or already revoked)", apperr.ErrNotFound, id)
	}
	return nil
}
