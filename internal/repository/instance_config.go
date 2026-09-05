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

// Update applies a partial update to the singleton, writing only the supplied
// (non-nil) columns plus updated_at. A patch with no fields set is a no-op.
func (r *InstanceConfigRepository) Update(ctx context.Context, patch models.InstanceConfigPatch) error {
	updates := map[string]any{}
	if patch.MMRLambda != nil {
		updates["mmr_lambda"] = *patch.MMRLambda
	}
	if patch.CandidatePool != nil {
		updates["candidate_pool"] = *patch.CandidatePool
	}
	if patch.SnippetChars != nil {
		updates["snippet_chars"] = *patch.SnippetChars
	}
	if patch.HistoryEnabled != nil {
		updates["history_enabled"] = *patch.HistoryEnabled
	}
	if patch.HistoryRetentionDays != nil {
		updates["history_retention_days"] = *patch.HistoryRetentionDays
	}
	if patch.StalenessDefault != nil {
		updates["staleness_default"] = *patch.StalenessDefault
	}
	if patch.DuplicateGuardDefault != nil {
		updates["duplicate_guard_default"] = *patch.DuplicateGuardDefault
	}
	if patch.CleanupScanDefault != nil {
		updates["cleanup_scan_default"] = *patch.CleanupScanDefault
	}
	if patch.DuplicateThreshold != nil {
		updates["duplicate_threshold"] = *patch.DuplicateThreshold
	}
	if patch.SelfServicePolicy != nil {
		updates["self_service_policy"] = *patch.SelfServicePolicy
	}
	if patch.SignupDomains != nil {
		updates["signup_domains"] = *patch.SignupDomains
	}
	if patch.AdminEmails != nil {
		updates["admin_emails"] = *patch.AdminEmails
	}
	if patch.CleanupEnabled != nil {
		updates["cleanup_enabled"] = *patch.CleanupEnabled
	}
	if patch.CleanupIntervalHours != nil {
		updates["cleanup_interval_hours"] = *patch.CleanupIntervalHours
	}
	if patch.RetentionSweepEnabled != nil {
		updates["retention_sweep_enabled"] = *patch.RetentionSweepEnabled
	}
	if patch.RetentionGraceDays != nil {
		updates["retention_grace_days"] = *patch.RetentionGraceDays
	}
	if patch.MetricsRetentionDays != nil {
		updates["metrics_retention_days"] = *patch.MetricsRetentionDays
	}
	if patch.RateLimitRPS != nil {
		updates["rate_limit_rps"] = *patch.RateLimitRPS
	}
	if patch.RateLimitBurst != nil {
		updates["rate_limit_burst"] = *patch.RateLimitBurst
	}
	if patch.TrustedProxyDepth != nil {
		updates["trusted_proxy_depth"] = *patch.TrustedProxyDepth
	}
	if patch.MaxRequestBytes != nil {
		updates["max_request_bytes"] = *patch.MaxRequestBytes
	}
	if patch.LogLevel != nil {
		updates["log_level"] = *patch.LogLevel
	}
	if patch.WebhookURL != nil {
		updates["webhook_url"] = *patch.WebhookURL
	}
	if patch.RequireConfigListener != nil {
		updates["require_config_listener"] = *patch.RequireConfigListener
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	if err := r.db.WithContext(ctx).
		Model(&models.InstanceConfig{ID: models.InstanceConfigSingletonID}).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update instance config: %w", err)
	}
	return nil
}

// SetHistoryEnabled flips the global mutation-history toggle, upserting the
// singleton row so it exists even on a pre-seed database.
func (r *InstanceConfigRepository) SetHistoryEnabled(ctx context.Context, enabled bool) error {
	cfg := models.InstanceConfig{ID: models.InstanceConfigSingletonID, HistoryEnabled: enabled, UpdatedAt: time.Now()}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"history_enabled", "updated_at"}),
	}).Create(&cfg).Error; err != nil {
		return fmt.Errorf("set history_enabled: %w", err)
	}
	return nil
}
