package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/models"
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
