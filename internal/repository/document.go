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

type DocumentRepository struct {
	db *gorm.DB
}

func NewDocumentRepository(db *gorm.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

// readTenants returns the tenant IDs to include in reads: the requesting tenant
// plus the common (bootstrap) pool of legacy/seed docs.
//
// idx_doc_tenant_path is per-tenant, so a tenant can override a bootstrap path
// with its own doc; GetByPath orders the requesting tenant first so it wins.
//
// Dedup side effect: list/scan can return two docs at one path with different
// tenant_ids (bootstrap vs tenant-owned) — the override pattern, not a bug. The
// cleanup agent should flag such cross-tenant pairs for review, never auto-merge.
func readTenants(tenantID uuid.UUID) []uuid.UUID {
	if tenantID == models.BootstrapTenantID {
		return []uuid.UUID{tenantID}
	}
	return []uuid.UUID{tenantID, models.BootstrapTenantID}
}

func (r *DocumentRepository) Create(ctx context.Context, doc *models.Document) error {
	return r.db.WithContext(ctx).Create(doc).Error
}

func (r *DocumentRepository) GetByPath(ctx context.Context, tenantID uuid.UUID, category string, subcategory *string, slug string) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).
		Where("tenant_id IN ?", readTenants(tenantID)).
		Where("archived_at IS NULL").
		Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	// Prefer the requesting tenant's doc over the common pool
	if err := q.Order(gorm.Expr("CASE WHEN tenant_id = ? THEN 0 ELSE 1 END", tenantID)).
		Preload("Sections", func(db *gorm.DB) *gorm.DB {
			return db.Order("ordinal ASC")
		}).First(&doc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
		}
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) GetByID(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*models.Document, error) {
	var doc models.Document
	if err := r.db.WithContext(ctx).
		Where("tenant_id IN ?", readTenants(tenantID)).
		Where("archived_at IS NULL").
		Preload("Sections", func(db *gorm.DB) *gorm.DB {
			return db.Order("ordinal ASC")
		}).
		First(&doc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) List(ctx context.Context, tenantID uuid.UUID, category *string, subcategory *string) ([]models.Document, error) {
	var docs []models.Document
	q := r.db.WithContext(ctx).Where("tenant_id IN ?", readTenants(tenantID)).Where("archived_at IS NULL")
	if category != nil {
		q = q.Where("category = ?", *category)
	}
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	}
	if err := q.Order("category, subcategory, slug").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (r *DocumentRepository) Save(ctx context.Context, tenantID uuid.UUID, doc *models.Document) error {
	if doc.TenantID != tenantID {
		return fmt.Errorf("%w: document tenant mismatch", apperr.ErrInvalidInput)
	}
	return r.db.WithContext(ctx).Save(doc).Error
}

func (r *DocumentRepository) Delete(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Delete(&models.Document{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
	}
	return nil
}

func (r *DocumentRepository) DeleteByPath(ctx context.Context, tenantID uuid.UUID, category string, subcategory *string, slug string) error {
	q := r.db.WithContext(ctx).Where("tenant_id = ? AND category = ? AND slug = ?", tenantID, category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	result := q.Delete(&models.Document{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: document %s/%s", apperr.ErrNotFound, category, slug)
	}
	return nil
}

// IndexDepth controls the aggregation level of GenerateIndex output.
type IndexDepth string

const (
	IndexDepthSummary  IndexDepth = "summary"
	IndexDepthCategory IndexDepth = "category"
	IndexDepthFull     IndexDepth = "full"
)

// IndexEntry is one row in the catalog produced by GenerateIndex.
type IndexEntry struct {
	Category    string  `json:"category"`
	Subcategory *string `json:"subcategory,omitempty"`
	DocCount    int     `json:"doc_count"`
	Topics      string  `json:"topics"`
}

// GenerateIndex produces a tiered catalog of documents for a tenant.
//
//   - summary:  one row per (category, subcategory) with COUNT and aggregated titles
//   - category: one row per document (DocCount=1, Topics=title), filtered by category
//   - full:     one row per document (DocCount=1, Topics=title), all categories
func (r *DocumentRepository) GenerateIndex(ctx context.Context, tenantID uuid.UUID, depth IndexDepth, category *string) ([]IndexEntry, error) {
	tenants := readTenants(tenantID)

	var sql string
	var args []any

	switch depth {
	case IndexDepthSummary:
		sql = `
			SELECT category,
			       subcategory,
			       COUNT(*)                          AS doc_count,
			       string_agg(title, ', ' ORDER BY title) AS topics
			FROM documents
			WHERE tenant_id IN ?
			  AND archived_at IS NULL
			  AND (?::text IS NULL OR category = ?)
			GROUP BY category, subcategory
			ORDER BY category, subcategory
		`
		args = []any{tenants, category, category}

	case IndexDepthCategory:
		sql = `
			SELECT category,
			       subcategory,
			       1          AS doc_count,
			       title      AS topics
			FROM documents
			WHERE tenant_id IN ?
			  AND archived_at IS NULL
			  AND (?::text IS NULL OR category = ?)
			ORDER BY category, subcategory, title
		`
		args = []any{tenants, category, category}

	case IndexDepthFull:
		sql = `
			SELECT category,
			       subcategory,
			       1          AS doc_count,
			       title      AS topics
			FROM documents
			WHERE tenant_id IN ?
			  AND archived_at IS NULL
			ORDER BY category, subcategory, title
		`
		args = []any{tenants}

	default:
		return nil, fmt.Errorf("%w: unknown index depth %q", apperr.ErrInvalidInput, depth)
	}

	var results []IndexEntry
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("generate index: %w", err)
	}
	return results, nil
}
