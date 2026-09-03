package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// MutationHistoryRepository appends and reads the mutation_history audit table.
type MutationHistoryRepository struct {
	db *gorm.DB
}

func NewMutationHistoryRepository(db *gorm.DB) *MutationHistoryRepository {
	return &MutationHistoryRepository{db: db}
}

// MutationEvent carries the fields logged for one document mutation.
type MutationEvent struct {
	TenantID     uuid.UUID
	DocumentID   uuid.UUID
	SectionID    *uuid.UUID
	DocumentPath string
	OpType       string
	ActorSubject string
	ActorEmail   *string
	APIKeyID     *uuid.UUID
	Before       *string
}

// Log appends one mutation history row. Best-effort — the caller decides whether
// a logging error is fatal (it never is; a dropped audit row beats a failed write).
func (r *MutationHistoryRepository) Log(ctx context.Context, ev MutationEvent) error {
	entry := &models.MutationHistory{
		TenantID:     ev.TenantID,
		DocumentID:   ev.DocumentID,
		SectionID:    ev.SectionID,
		DocumentPath: ev.DocumentPath,
		OpType:       ev.OpType,
		ActorSubject: ev.ActorSubject,
		ActorEmail:   ev.ActorEmail,
		APIKeyID:     ev.APIKeyID,
		Before:       ev.Before,
	}
	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("log mutation history: %w", err)
	}
	return nil
}

// ListByDocument returns a document's history newest-first, capped at limit (<=0 = uncapped).
func (r *MutationHistoryRepository) ListByDocument(ctx context.Context, docID uuid.UUID, limit int) ([]models.MutationHistory, error) {
	q := r.db.WithContext(ctx).Where("document_id = ?", docID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []models.MutationHistory
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list mutation history: %w", err)
	}
	return rows, nil
}

// PruneOlderThan hard-deletes history rows created before cutoff, returning the count.
func (r *MutationHistoryRepository) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&models.MutationHistory{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune mutation history: %w", res.Error)
	}
	return res.RowsAffected, nil
}
