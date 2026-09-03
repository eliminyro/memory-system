package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type EdgeRepository struct {
	db *gorm.DB
}

func NewEdgeRepository(db *gorm.DB) *EdgeRepository {
	return &EdgeRepository{db: db}
}

// EdgeListItem is one row of ListByDocument: the edge plus the OTHER endpoint's
// identity and archived flag. Direction is relative to the queried document.
type EdgeListItem struct {
	EdgeID                uuid.UUID `json:"edge_id"`
	EdgeType              string    `json:"edge_type"`
	Direction             string    `json:"direction"`
	OtherDocumentID       uuid.UUID `json:"other_document_id"`
	OtherDocumentPath     string    `json:"other_document_path"`
	OtherDocumentTitle    string    `json:"other_document_title"`
	OtherDocumentArchived bool      `json:"other_document_archived"`
	ActorSubject          string    `json:"actor_subject"`
	CreatedAt             time.Time `json:"created_at"`
}

// Create inserts an edge. On a unique-triple conflict it returns the EXISTING
// edge with created=false, so the caller runs no second side effect (idempotent).
func (r *EdgeRepository) Create(ctx context.Context, e *models.Edge) (*models.Edge, bool, error) {
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_document_id"}, {Name: "target_document_id"}, {Name: "edge_type"}},
		DoNothing: true,
	}).Create(e)
	if res.Error != nil {
		return nil, false, fmt.Errorf("create edge: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return e, true, nil
	}
	existing, err := r.getByTriple(ctx, e.SourceDocumentID, e.TargetDocumentID, e.EdgeType)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (r *EdgeRepository) getByTriple(ctx context.Context, source, target uuid.UUID, edgeType string) (*models.Edge, error) {
	var e models.Edge
	if err := r.db.WithContext(ctx).
		Where("source_document_id = ? AND target_document_id = ? AND edge_type = ?", source, target, edgeType).
		First(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: edge %s->%s (%s)", apperr.ErrNotFound, source, target, edgeType)
		}
		return nil, err
	}
	return &e, nil
}

func (r *EdgeRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Edge, error) {
	var e models.Edge
	if err := r.db.WithContext(ctx).First(&e, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: edge %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &e, nil
}

// Delete removes one edge by id, returning rows-affected so the caller maps
// 0 -> ErrNotFound. Deleting a supersedes edge does NOT un-archive its target.
func (r *EdgeRepository) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).Delete(&models.Edge{}, id)
	return res.RowsAffected, res.Error
}

// ListByDocument returns docID's edges (both directions) with the other endpoint's
// identity, but only when that endpoint's tenant is in readTenants — an out-of-scope
// sibling is omitted, not leaked. No archived_at filter: in-scope archived endpoints show.
func (r *EdgeRepository) ListByDocument(ctx context.Context, docID uuid.UUID, readTenants []uuid.UUID) ([]EdgeListItem, error) {
	const sql = `
		SELECT e.id AS edge_id, e.edge_type, 'outgoing' AS direction,
		       e.target_document_id AS other_document_id,
		       d.category || '/' || COALESCE(d.subcategory || '/', '') || d.slug AS other_document_path,
		       COALESCE(d.title, '') AS other_document_title,
		       (d.archived_at IS NOT NULL) AS other_document_archived,
		       e.actor_subject, e.created_at
		FROM document_edges e
		LEFT JOIN documents d ON d.id = e.target_document_id
		WHERE e.source_document_id = ?
		  AND (d.id IS NULL OR d.tenant_id IN ?)
		UNION ALL
		SELECT e.id AS edge_id, e.edge_type, 'incoming' AS direction,
		       e.source_document_id AS other_document_id,
		       d.category || '/' || COALESCE(d.subcategory || '/', '') || d.slug AS other_document_path,
		       COALESCE(d.title, '') AS other_document_title,
		       (d.archived_at IS NOT NULL) AS other_document_archived,
		       e.actor_subject, e.created_at
		FROM document_edges e
		LEFT JOIN documents d ON d.id = e.source_document_id
		WHERE e.target_document_id = ?
		  AND (d.id IS NULL OR d.tenant_id IN ?)
		ORDER BY created_at DESC
	`
	var items []EdgeListItem
	if err := r.db.WithContext(ctx).Raw(sql, docID, readTenants, docID, readTenants).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list edges by document: %w", err)
	}
	return items, nil
}
