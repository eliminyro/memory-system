package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/eliminyro/memory-system/internal/models"
)

// InstanceConfigRepository reads and writes the singleton instance_config row.
type InstanceConfigRepository struct {
	db *gorm.DB
}

func NewInstanceConfigRepository(db *gorm.DB) *InstanceConfigRepository {
	return &InstanceConfigRepository{db: db}
}

// Get returns the singleton config row, creating it (defaults) if absent.
func (r *InstanceConfigRepository) Get(ctx context.Context) (*models.InstanceConfig, error) {
	var cfg models.InstanceConfig
	if err := r.db.WithContext(ctx).
		Where(models.InstanceConfig{ID: models.InstanceConfigSingletonID}).
		FirstOrCreate(&cfg).Error; err != nil {
		return nil, fmt.Errorf("get instance config: %w", err)
	}
	return &cfg, nil
}

// SetAccessRetentionEnabled flips the global access-recency eviction toggle,
// upserting the singleton row so it exists even on a pre-seed database.
func (r *InstanceConfigRepository) SetAccessRetentionEnabled(ctx context.Context, enabled bool) error {
	cfg := models.InstanceConfig{ID: models.InstanceConfigSingletonID, AccessRetentionEnabled: enabled, UpdatedAt: time.Now()}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"access_retention_enabled", "updated_at"}),
	}).Create(&cfg).Error; err != nil {
		return fmt.Errorf("set access_retention_enabled: %w", err)
	}
	return nil
}
