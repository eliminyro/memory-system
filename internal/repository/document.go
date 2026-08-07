package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
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

// ReadTenants is the exported form of readTenants for service-layer WRITE-path
// callers of the set-based read methods (GetByPath/GetByID/List): it preserves
// the single-tenant + common-pool scope so writes and the guest-editor
// common-pool path keep their exact pre-aggregation behavior. READ callers pass
// the service-computed readable set instead.
func ReadTenants(tenantID uuid.UUID) []uuid.UUID {
	return readTenants(tenantID)
}

func (r *DocumentRepository) Create(ctx context.Context, doc *models.Document) error {
	return r.db.WithContext(ctx).Create(doc).Error
}

// GetByPath resolves a document by path across the given tenant-id set. `home`
// is the requesting tenant used only for ordering: a doc owned by `home` is
// preferred over one in the common pool or another readable tenant when the
// same path exists in several.
func (r *DocumentRepository) GetByPath(ctx context.Context, tenantIDs []uuid.UUID, home uuid.UUID, category string, subcategory *string, slug string) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).
		Where("tenant_id IN ?", tenantIDs).
		Where("archived_at IS NULL").
		Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	// Prefer the requesting (home) tenant's doc over the common pool / others
	if err := q.Order(gorm.Expr("CASE WHEN tenant_id = ? THEN 0 ELSE 1 END", home)).
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

// GetByID resolves a document by primary key, scoped to the given tenant-id set.
func (r *DocumentRepository) GetByID(ctx context.Context, tenantIDs []uuid.UUID, id uuid.UUID) (*models.Document, error) {
	var doc models.Document
	if err := r.db.WithContext(ctx).
		Where("tenant_id IN ?", tenantIDs).
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

// List returns documents across the given tenant-id set, optionally filtered.
func (r *DocumentRepository) List(ctx context.Context, tenantIDs []uuid.UUID, category *string, subcategory *string) ([]models.Document, error) {
	var docs []models.Document
	q := r.db.WithContext(ctx).Where("tenant_id IN ?", tenantIDs).Where("archived_at IS NULL")
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

// Save persists doc (and, via GORM associations, its sections) scoped to tenantID.
// gorm's db.Save is a PK-keyed UPDATE with no tenant_id predicate, so before saving
// we verify the row actually exists under tenantID: a cross-tenant id is un-writable
// (returns ErrNotFound) rather than silently overwriting another tenant's document.
// The mismatch guard stays for callers that pass a doc whose TenantID differs from
// the write tenant.
func (r *DocumentRepository) Save(ctx context.Context, tenantID uuid.UUID, doc *models.Document) error {
	if doc.TenantID != tenantID {
		return fmt.Errorf("%w: document tenant mismatch", apperr.ErrInvalidInput)
	}
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&models.Document{}).
		Where("id = ? AND tenant_id = ?", doc.ID, tenantID).
		Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: document %s", apperr.ErrNotFound, doc.ID)
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
	// Prune the document's authz tuples: the document->tenant parent edge and any
	// guest viewer/editor grants. relation_tuples has no FK cascade to documents
	// (mirrors the tenant-delete prune in tenant.go:76-83).
	if err := r.db.WithContext(ctx).Exec(
		`DELETE FROM relation_tuples WHERE object_type = ? AND object_id = ?`,
		authz.TypeDocument, id.String()).Error; err != nil {
		return fmt.Errorf("delete document tuples: %w", err)
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

// GenerateIndex produces a tiered catalog of documents over a tenant-id set
// (the caller's readable scope), filtering tenant_id IN (?).
//
//   - summary:  one row per (category, subcategory) with COUNT and aggregated titles
//   - category: one row per document (DocCount=1, Topics=title), filtered by category
//   - full:     one row per document (DocCount=1, Topics=title), all categories
func (r *DocumentRepository) GenerateIndex(ctx context.Context, tenantIDs []uuid.UUID, depth IndexDepth, category *string) ([]IndexEntry, error) {
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
		args = []any{tenantIDs, category, category}

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
		args = []any{tenantIDs, category, category}

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
		args = []any{tenantIDs}

	default:
		return nil, fmt.Errorf("%w: unknown index depth %q", apperr.ErrInvalidInput, depth)
	}

	var results []IndexEntry
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("generate index: %w", err)
	}
	return results, nil
}
