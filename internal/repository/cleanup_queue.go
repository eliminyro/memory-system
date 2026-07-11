package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type CleanupQueueRepository struct {
	db *gorm.DB
}

func NewCleanupQueueRepository(db *gorm.DB) *CleanupQueueRepository {
	return &CleanupQueueRepository{db: db}
}

// ListPending returns unresolved queue entries, newest first.
func (r *CleanupQueueRepository) ListPending(ctx context.Context, tenantID uuid.UUID, limit int) ([]models.CleanupQueue, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []models.CleanupQueue
	if err := r.db.WithContext(ctx).
		Where("tenant_id IN ? AND resolved_at IS NULL", readTenants(tenantID)).
		Order("detected_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending cleanup: %w", err)
	}
	return rows, nil
}

// ListAll returns queue entries for the tenant, most recent first.
func (r *CleanupQueueRepository) ListAll(ctx context.Context, tenantID uuid.UUID, limit int) ([]models.CleanupQueue, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []models.CleanupQueue
	if err := r.db.WithContext(ctx).
		Where("tenant_id IN ?", readTenants(tenantID)).
		Order("detected_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list cleanup queue: %w", err)
	}
	return rows, nil
}

// NormalizePair returns (lo, hi) such that lo.String() <= hi.String(). Exposed
// so callers and tests can reason about the invariant that cleanup_queue rows
// always store the smaller UUID in doc_a_id and the larger in doc_b_id,
// preventing two rows for the same unordered pair.
func NormalizePair(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if b.String() < a.String() {
		return b, a
	}
	return a, b
}

// Upsert inserts a new queue entry for a (tenant, doc_a, doc_b) pair only if
// there isn't already an unresolved row for the same unordered pair. Returns
// true if a row was inserted, false if a matching pending entry already exists.
func (r *CleanupQueueRepository) Upsert(ctx context.Context, entry *models.CleanupQueue) (bool, error) {
	lo, hi := NormalizePair(entry.DocAID, entry.DocBID)
	entry.DocAID, entry.DocBID = lo, hi

	var existing models.CleanupQueue
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND doc_a_id = ? AND doc_b_id = ? AND resolved_at IS NULL", entry.TenantID, lo, hi).
		First(&existing).Error
	if err == nil {
		// Refresh similarity if the newer scan found a higher value — cheap book-keeping.
		if entry.Similarity > existing.Similarity {
			if err := r.db.WithContext(ctx).
				Model(&existing).
				Update("similarity", entry.Similarity).Error; err != nil {
				return false, fmt.Errorf("refresh similarity: %w", err)
			}
		}
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("check existing: %w", err)
	}

	// Suppress re-enqueue when the operator already dismissed this pair as a
	// false positive or chose to ignore it (audit #19). Without this, every
	// nightly scan re-adds a pair the operator explicitly rejected. A "merged"
	// resolution is not suppressed — that pair can't re-form once one doc is gone.
	var dismissed int64
	if err := r.db.WithContext(ctx).
		Model(&models.CleanupQueue{}).
		Where("tenant_id = ? AND doc_a_id = ? AND doc_b_id = ? AND resolution IN ?",
			entry.TenantID, lo, hi,
			[]string{models.CleanupResolutionFalsePositive, models.CleanupResolutionIgnored}).
		Count(&dismissed).Error; err != nil {
		return false, fmt.Errorf("check dismissed: %w", err)
	}
	if dismissed > 0 {
		return false, nil
	}

	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return false, fmt.Errorf("insert cleanup row: %w", err)
	}
	return true, nil
}

// GetByID returns a single queue entry addressed by id, scoped to the caller's
// accessible tenants (own tenant + common pool). Returns ErrNotFound when the
// entry does not exist in scope. Used by the service to resolve the entry's
// referenced document before an authorization Check.
func (r *CleanupQueueRepository) GetByID(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*models.CleanupQueue, error) {
	var row models.CleanupQueue
	if err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id IN ?", id, readTenants(tenantID)).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: cleanup queue entry %s", apperr.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get cleanup entry: %w", err)
	}
	return &row, nil
}

// Resolve marks a queue entry as resolved with the given resolution/note. If
// resolution is "merged", set mergedInto to the surviving doc ID.
func (r *CleanupQueueRepository) Resolve(ctx context.Context, tenantID uuid.UUID, id uuid.UUID, resolution string, note string, mergedInto *uuid.UUID) error {
	updates := map[string]any{
		"resolved_at":     gorm.Expr("NOW()"),
		"resolution":      resolution,
		"resolution_note": note,
	}
	if mergedInto != nil {
		updates["merged_into"] = *mergedInto
	}
	result := r.db.WithContext(ctx).
		Model(&models.CleanupQueue{}).
		Where("id = ? AND tenant_id IN ?", id, readTenants(tenantID)).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("resolve cleanup: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: cleanup queue entry %s", apperr.ErrNotFound, id)
	}
	return nil
}

// CountPending returns the number of unresolved entries for the tenant.
func (r *CleanupQueueRepository) CountPending(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&models.CleanupQueue{}).
		Where("tenant_id IN ? AND resolved_at IS NULL", readTenants(tenantID)).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	return n, nil
}
