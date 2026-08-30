package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

type OverrideLogRepository struct {
	db *gorm.DB
}

func NewOverrideLogRepository(db *gorm.DB) *OverrideLogRepository {
	return &OverrideLogRepository{db: db}
}

type OverrideEvent struct {
	TenantID     uuid.UUID
	Tool         string
	TargetID     *uuid.UUID
	OverrideType string
	Reason       string
	APIKeyID     *uuid.UUID
}

// Log records a single override event. Best-effort — caller decides whether to
// fail on logging errors (usually not; a dropped audit row beats a dropped request).
func (r *OverrideLogRepository) Log(ctx context.Context, ev OverrideEvent) error {
	entry := &models.OverrideLog{
		TenantID:     ev.TenantID,
		Tool:         ev.Tool,
		TargetID:     ev.TargetID,
		OverrideType: ev.OverrideType,
		Reason:       ev.Reason,
		APIKeyID:     ev.APIKeyID,
	}
	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("log override: %w", err)
	}
	return nil
}
