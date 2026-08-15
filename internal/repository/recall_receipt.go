package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type RecallReceiptRepository struct {
	db *gorm.DB
}

func NewRecallReceiptRepository(db *gorm.DB) *RecallReceiptRepository {
	return &RecallReceiptRepository{db: db}
}

// Create inserts a receipt naming the sections served by tenantID's search and
// returns its generated recall_id.
func (r *RecallReceiptRepository) Create(ctx context.Context, tenantID uuid.UUID, sectionIDs []uuid.UUID) (uuid.UUID, error) {
	receipt := &models.RecallReceipt{
		TenantID:   tenantID,
		SectionIDs: models.UUIDArray(sectionIDs),
	}
	if err := r.db.WithContext(ctx).Create(receipt).Error; err != nil {
		return uuid.Nil, fmt.Errorf("create recall receipt: %w", err)
	}
	return receipt.RecallID, nil
}

// GetByID loads a receipt scoped to tenantID. ErrNotFound on a missing or
// cross-tenant id — the caller must never learn a recall_id exists elsewhere.
func (r *RecallReceiptRepository) GetByID(ctx context.Context, tenantID, recallID uuid.UUID) (*models.RecallReceipt, error) {
	var receipt models.RecallReceipt
	if err := r.db.WithContext(ctx).
		Where("recall_id = ? AND tenant_id = ?", recallID, tenantID).
		First(&receipt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: recall %s", apperr.ErrNotFound, recallID)
		}
		return nil, err
	}
	return &receipt, nil
}

// creditHitSQL / creditMissSQL scope crediting to sections whose owning
// document's tenant is EITHER the receipt's own tenant OR any SHARED-type
// tenant — NEVER another PERSONAL tenant. Without this a served section from
// an unrelated personal tenant would be credited just by appearing in a
// cross-tenant search result, a manipulation channel once Phase B reads these
// counts into ranking. The common pool is shared-type, so it credits normally.
const creditHitSQL = `
	UPDATE sections SET hit_count = hit_count + 1
	WHERE id IN ?
	  AND document_id IN (
	    SELECT d.id FROM documents d
	    JOIN tenants t ON t.id = d.tenant_id
	    WHERE d.tenant_id = ? OR t.type = ?
	  )
`

const creditMissSQL = `
	UPDATE sections SET miss_count = miss_count + 1
	WHERE id IN ?
	  AND document_id IN (
	    SELECT d.id FROM documents d
	    JOIN tenants t ON t.id = d.tenant_id
	    WHERE d.tenant_id = ? OR t.type = ?
	  )
`

// ReportOutcome atomically marks a receipt reported and credits outcome on the
// sections it named, scoped to tenantID. The mark-reported UPDATE (guarded by
// "reported_at IS NULL") and the section credit run in ONE transaction, so two
// concurrent reports of the same recall_id can never both credit: Postgres
// serializes the competing UPDATEs on the receipt row, and only the caller
// that actually flips reported_at from NULL to non-NULL proceeds to credit —
// closing the GetByID-then-check TOCTOU a separate read-then-write would have.
// Returns apperr.ErrNotFound when no receipt exists for (recallID, tenantID);
// an already-reported receipt is a silent no-op (nil error, no credit).
func (r *RecallReceiptRepository) ReportOutcome(ctx context.Context, tenantID, recallID uuid.UUID, outcome string) error {
	sql := creditHitSQL
	if outcome == models.RecallOutcomeFailure {
		sql = creditMissSQL
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			SectionIDs models.UUIDArray `gorm:"column:section_ids"`
		}
		if err := tx.Raw(
			`UPDATE recall_receipts SET reported_at = NOW()
			 WHERE recall_id = ? AND tenant_id = ? AND reported_at IS NULL
			 RETURNING section_ids`,
			recallID, tenantID,
		).Scan(&row).Error; err != nil {
			return fmt.Errorf("mark receipt reported: %w", err)
		}
		if len(row.SectionIDs) == 0 {
			// The guarded UPDATE touched no row: either no receipt exists for this
			// (recallID, tenantID), or it was already reported (a concurrent
			// winner, or a genuine duplicate call). A real receipt is never
			// created with an empty section list (recordRecallReceipt only fires
			// on non-empty results), so re-checking existence — in the SAME
			// transaction, no new TOCTOU window — safely distinguishes the two.
			var exists int64
			if err := tx.Model(&models.RecallReceipt{}).
				Where("recall_id = ? AND tenant_id = ?", recallID, tenantID).
				Count(&exists).Error; err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("%w: recall %s", apperr.ErrNotFound, recallID)
			}
			return nil // already reported: idempotent no-op, no credit
		}

		if err := tx.Exec(sql, []uuid.UUID(row.SectionIDs), tenantID, models.TenantTypeShared).Error; err != nil {
			return fmt.Errorf("credit %s outcome: %w", outcome, err)
		}
		return nil
	})
}

// PruneExpired deletes receipts older than before. Deletes rows only — never
// touches hit_count/miss_count, which live on sections independent of receipt
// lifetime (design D3/spec: pruning must not alter already-applied counts).
func (r *RecallReceiptRepository) PruneExpired(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.RecallReceipt{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune recall receipts: %w", res.Error)
	}
	return res.RowsAffected, nil
}
