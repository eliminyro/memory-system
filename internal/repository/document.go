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
// idx_doc_tenant_path_active is per-tenant, so a tenant can override a bootstrap path
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

// GetByIDIncludingArchived resolves a document by id across the tenant-id set
// WITHOUT the archived_at filter, so an edge can point at (or supersede) a doc
// that is already archived. Sections are not preloaded.
func (r *DocumentRepository) GetByIDIncludingArchived(ctx context.Context, tenantIDs []uuid.UUID, id uuid.UUID) (*models.Document, error) {
	var doc models.Document
	if err := r.db.WithContext(ctx).
		Where("tenant_id IN ?", tenantIDs).
		First(&doc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: document %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &doc, nil
}

// ArchiveByID archives one document by id. The AND archived_at IS NULL guard
// makes it idempotent: superseding an already-archived target is a 0-row no-op.
func (r *DocumentRepository) ArchiveByID(ctx context.Context, id uuid.UUID, reason string) (int64, error) {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE documents SET archived_at = now(), archive_reason = ? WHERE id = ? AND archived_at IS NULL`,
		reason, id,
	)
	if res.Error != nil {
		return 0, fmt.Errorf("archive by id: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ListOrderColumns maps each allowed order_by value to its SQL column, so a
// caller string is validated against these keys and nothing caller-controlled
// reaches the ORDER BY clause; id is appended separately as the tiebreaker.
var ListOrderColumns = map[string]string{
	"slug":       "slug",
	"created_at": "created_at",
	"updated_at": "updated_at",
	"title":      "title",
}

// List returns documents across the tenant-id set, filtered by category,
// subcategory (exact) or subcategoryPrefix (subtree), and pre-escaped slugPrefix,
// ordered per orderBy/order (limit <= 0 = all; every order ends in id, D6).
func (r *DocumentRepository) List(ctx context.Context, tenantIDs []uuid.UUID, category, subcategory *string, slugPrefix, subcategoryPrefix, orderBy, order string, limit, offset int) ([]models.Document, error) {
	var docs []models.Document
	q := r.db.WithContext(ctx).Where("tenant_id IN ?", tenantIDs).Where("archived_at IS NULL")
	if category != nil {
		q = q.Where("category = ?", *category)
	}
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	}
	if slugPrefix != "" {
		q = q.Where("slug LIKE ?", slugPrefix+"%")
	}
	if subcategoryPrefix != "" {
		q = q.Where("subcategory LIKE ? OR subcategory LIKE ?", subcategoryPrefix, subcategoryPrefix+"/%")
	}
	q = q.Order(listOrderClause(orderBy, order))
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	if err := q.Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

// listOrderClause builds a safe ORDER BY from the allowlist: an empty orderBy
// keeps the legacy composite order, otherwise the mapped column plus direction,
// always ending in id so the sort key stays unique.
func listOrderClause(orderBy, order string) string {
	col, ok := ListOrderColumns[orderBy]
	if !ok {
		return "category, subcategory, slug, id"
	}
	dir := "ASC"
	if order == "desc" {
		dir = "DESC"
	}
	return col + " " + dir + ", id"
}

// LatestByDocType returns the newest non-archived document of docType in the
// tenant set, matched by subcategory scope (nil ⇒ IS NULL), excluding excludeID.
// Backs rule-driven chain_previous linking. (nil,nil) when none.
func (r *DocumentRepository) LatestByDocType(ctx context.Context, tenantIDs []uuid.UUID, docType string, subcategory *string, excludeID *uuid.UUID) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).
		Where("doc_type = ?", docType).
		Where("tenant_id IN ?", tenantIDs).
		Where("archived_at IS NULL")
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	if excludeID != nil {
		q = q.Where("id <> ?", *excludeID)
	}
	if err := q.Order("created_at DESC").First(&doc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &doc, nil
}

// LatestHandoff returns the newest non-archived handoff in the tenant set (nil,nil
// if none), excluding excludeID. anyProject omits the project filter; else matches
// subcategory exactly (nil ⇒ IS NULL) — the exact-project chain key auto-chain needs.
func (r *DocumentRepository) LatestHandoff(ctx context.Context, tenantIDs []uuid.UUID, subcategory *string, anyProject bool, excludeID *uuid.UUID) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).
		Where("doc_type = ?", models.DocTypeHandoff).
		Where("tenant_id IN ?", tenantIDs).
		Where("archived_at IS NULL")
	if !anyProject {
		q = q.Where("category = ?", "handoffs")
		if subcategory != nil {
			q = q.Where("subcategory = ?", *subcategory)
		} else {
			q = q.Where("subcategory IS NULL")
		}
	}
	if excludeID != nil {
		q = q.Where("id <> ?", *excludeID)
	}
	err := q.Order("created_at DESC").
		Preload("Sections", func(db *gorm.DB) *gorm.DB {
			return db.Order("ordinal ASC")
		}).
		First(&doc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &doc, nil
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
	// cleanup_queue references docs by doc_a_id/doc_b_id with no FK cascade, so a
	// pending near-dup pair would dangle after an interactive delete — prune it too.
	if err := r.db.WithContext(ctx).Exec(
		`DELETE FROM cleanup_queue WHERE doc_a_id = ? OR doc_b_id = ?`, id, id).Error; err != nil {
		return fmt.Errorf("delete document cleanup queue: %w", err)
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
	TenantID    uuid.UUID `json:"tenant_id"`
	TenantName  string    `json:"tenant_name,omitempty"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	DocCount    int       `json:"doc_count"`
	Topics      string    `json:"topics"`
}

// GenerateIndex produces a tiered catalog of documents over a tenant-id set
// (the caller's readable scope), filtering tenant_id IN (?).
//
//   - summary:  one row per (tenant, category, subcategory) with COUNT and aggregated titles
//   - category: one row per document (DocCount=1, Topics=title), filtered by category
//   - full:     one row per document (DocCount=1, Topics=title), all categories
func (r *DocumentRepository) GenerateIndex(ctx context.Context, tenantIDs []uuid.UUID, depth IndexDepth, category *string) ([]IndexEntry, error) {
	var sql string
	var args []any

	switch depth {
	case IndexDepthSummary:
		sql = `
			SELECT d.tenant_id                                 AS tenant_id,
			       t.name                                      AS tenant_name,
			       d.category                                  AS category,
			       d.subcategory                               AS subcategory,
			       COUNT(*)                                    AS doc_count,
			       string_agg(d.title, ', ' ORDER BY d.title)  AS topics
			FROM documents d
			LEFT JOIN tenants t ON t.id = d.tenant_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			GROUP BY d.tenant_id, t.name, d.category, d.subcategory
			ORDER BY d.category, d.subcategory, t.name
		`
		args = []any{tenantIDs, category, category}

	case IndexDepthCategory:
		sql = `
			SELECT d.tenant_id   AS tenant_id,
			       t.name        AS tenant_name,
			       d.category    AS category,
			       d.subcategory AS subcategory,
			       1             AS doc_count,
			       d.title       AS topics
			FROM documents d
			LEFT JOIN tenants t ON t.id = d.tenant_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			ORDER BY d.category, d.subcategory, d.title
		`
		args = []any{tenantIDs, category, category}

	case IndexDepthFull:
		sql = `
			SELECT d.tenant_id   AS tenant_id,
			       t.name        AS tenant_name,
			       d.category    AS category,
			       d.subcategory AS subcategory,
			       1             AS doc_count,
			       d.title       AS topics
			FROM documents d
			LEFT JOIN tenants t ON t.id = d.tenant_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			ORDER BY d.category, d.subcategory, d.title
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

// TouchAccessed day-granular bumps last_accessed_at=now() for the given docs,
// skipping any already touched today so repeat same-day serves cost <=1 write
// (D2). Empty input is a no-op. Plain []uuid.UUID + GORM IN ? matches the column.
func (r *DocumentRepository) TouchAccessed(ctx context.Context, docIDs []uuid.UUID) error {
	if len(docIDs) == 0 {
		return nil
	}
	const sql = `
		UPDATE documents
		SET last_accessed_at = now()
		WHERE id IN ?
		  AND (last_accessed_at IS NULL OR last_accessed_at < date_trunc('day', now()))
	`
	if err := r.db.WithContext(ctx).Exec(sql, docIDs).Error; err != nil {
		return fmt.Errorf("touch accessed: %w", err)
	}
	return nil
}
