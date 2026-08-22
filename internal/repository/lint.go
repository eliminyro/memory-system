package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

type LintRepository struct {
	db *gorm.DB
}

func NewLintRepository(db *gorm.DB) *LintRepository {
	return &LintRepository{db: db}
}

type LintSeverity string

const (
	LintSeverityWarning LintSeverity = "warning"
	LintSeverityInfo    LintSeverity = "info"
)

type LintFinding struct {
	Check        string       `json:"check"`
	Severity     LintSeverity `json:"severity"`
	DocumentPath string       `json:"document_path"`
	Message      string       `json:"message"`
}

type LintThresholds struct {
	StaleDays            int     `json:"stale_days"`
	SparseMinSections    int     `json:"sparse_min_sections"`
	SparseMinContentLen  int     `json:"sparse_min_content_len"`
	DuplicateSimilarity  float64 `json:"duplicate_similarity"`
	EmptyCategoryMinDocs int     `json:"empty_category_min_docs"`

	// Bounds on the near-duplicate section self-join (audit #10). Zero means
	// "use the package default" so existing callers stay unaffected.
	DuplicateMaxSections int `json:"duplicate_max_sections,omitempty"`
	DuplicateNeighbors   int `json:"duplicate_neighbors,omitempty"`
	DuplicateMaxPairs    int `json:"duplicate_max_pairs,omitempty"`
}

func DefaultLintThresholds() LintThresholds {
	return LintThresholds{
		StaleDays:            90,
		SparseMinSections:    2,
		SparseMinContentLen:  100,
		DuplicateSimilarity:  0.92,
		EmptyCategoryMinDocs: 2,
		DuplicateMaxSections: defaultDupMaxSections,
		DuplicateNeighbors:   defaultDupNeighbors,
		DuplicateMaxPairs:    defaultDupMaxPairs,
	}
}

// Bounds on the near-duplicate section self-join. Unbounded it is O(N^2) in a
// tenant's sections and can pin a backend for minutes (CheckNearDuplicates audit
// #10, nightly FindNearDuplicatePairs audit #14). The candidate pre-filter
// replaces the all-pairs cross join with a per-section HNSW k-NN probe, caps the
// outer scan, and caps the result set.
const (
	defaultDupMaxSections = 5000             // candidate sections scanned (outer side of the join)
	defaultDupNeighbors   = 20               // nearest-neighbour sections probed per candidate via the HNSW index
	defaultDupMaxPairs    = 1000             // cap on returned document pairs
	dupScanTimeout        = 30 * time.Second // server-side statement_timeout + matching context deadline
	dupScanEfSearch       = 100              // HNSW ef_search: keep KNN recall high under the tenant filter
)

// dupScanCaps resolves the configured bounds, clamping each field to
// [1, default]. A caller may only LOWER a bound below the package default (to
// scan less); it can never raise one above the default. The default is the
// ceiling the audit fix intended, so a hostile/careless caller can't pass a
// huge value and blow up the O(N^2) scan. Zero/negative falls back to default.
func (t LintThresholds) dupScanCaps() (maxSections, neighbors, maxPairs int) {
	maxSections, neighbors, maxPairs = t.DuplicateMaxSections, t.DuplicateNeighbors, t.DuplicateMaxPairs
	if maxSections <= 0 || maxSections > defaultDupMaxSections {
		maxSections = defaultDupMaxSections
	}
	if neighbors <= 0 || neighbors > defaultDupNeighbors {
		neighbors = defaultDupNeighbors
	}
	if maxPairs <= 0 || maxPairs > defaultDupMaxPairs {
		maxPairs = defaultDupMaxPairs
	}
	return
}

// runBoundedScan runs a near-duplicate query under a server-side statement_timeout
// and a matching context deadline, so a pathological plan can't run unbounded.
// In a transaction because SET LOCAL only lives for the current transaction.
func (r *LintRepository) runBoundedScan(ctx context.Context, dest any, sql string, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, dupScanTimeout)
	defer cancel()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", dupScanTimeout.Milliseconds())).Error; err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf("SET LOCAL hnsw.ef_search = %d", dupScanEfSearch)).Error; err != nil {
			return err
		}
		return tx.Raw(sql, args...).Scan(dest).Error
	})
}

// CheckStale finds documents that have not been updated within the stale_days threshold.
func (r *LintRepository) CheckStale(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT category, subcategory, slug, title, updated_at,
		       EXTRACT(DAY FROM NOW() - updated_at)::int AS days_since_update
		FROM documents
		WHERE tenant_id IN ?
		  AND archived_at IS NULL
		  AND doc_type <> ALL(?)
		  AND updated_at < NOW() - make_interval(days => ?)
		ORDER BY updated_at ASC
	`

	type row struct {
		Category        string  `gorm:"column:category"`
		Subcategory     *string `gorm:"column:subcategory"`
		Slug            string  `gorm:"column:slug"`
		DaysSinceUpdate int     `gorm:"column:days_since_update"`
	}

	var rows []row
	episodic := pq.StringArray(models.EpisodicDocTypes())
	if err := r.db.WithContext(ctx).Raw(sql, tenants, episodic, thresholds.StaleDays).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check stale: %w", err)
	}

	findings := make([]LintFinding, 0, len(rows))
	for _, r := range rows {
		findings = append(findings, LintFinding{
			Check:        "stale",
			Severity:     LintSeverityInfo,
			DocumentPath: models.BuildPath(r.Category, r.Subcategory, r.Slug),
			Message:      fmt.Sprintf("document has not been updated in %d days (threshold: %d)", r.DaysSinceUpdate, thresholds.StaleDays),
		})
	}
	return findings, nil
}

// CheckAccessCold is the read-only access-recency eviction dry-run (D5): the
// ArchiveAccessCold predicate (COALESCE(last_accessed_at,created_at) < the
// doc_type's cutoff, unpinned, non-episodic, unarchived, single tenant) minus the
// UPDATE. Per-doc_type cutoffs so the preview uses the same per-category windows
// as the sweep; one SELECT per doc_type in cutoffs.
func (r *LintRepository) CheckAccessCold(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]time.Time) ([]LintFinding, error) {
	episodic := pq.StringArray(models.EpisodicDocTypes())
	const sql = `
		SELECT category, subcategory, slug,
		       EXTRACT(DAY FROM NOW() - COALESCE(last_accessed_at, created_at))::int AS days_cold
		FROM documents
		WHERE tenant_id = ?
		  AND doc_type = ?
		  AND doc_type <> ALL(?)
		  AND archived_at IS NULL
		  AND NOT pinned
		  AND COALESCE(last_accessed_at, created_at) < ?
		ORDER BY COALESCE(last_accessed_at, created_at) ASC
	`

	type row struct {
		Category    string  `gorm:"column:category"`
		Subcategory *string `gorm:"column:subcategory"`
		Slug        string  `gorm:"column:slug"`
		DaysCold    int     `gorm:"column:days_cold"`
	}

	var findings []LintFinding
	for docType, cutoff := range cutoffs {
		var rows []row
		if err := r.db.WithContext(ctx).Raw(sql, tenantID, docType, episodic, cutoff).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("check access cold (%s): %w", docType, err)
		}
		for _, r := range rows {
			findings = append(findings, LintFinding{
				Check:        "access_cold",
				Severity:     LintSeverityWarning,
				DocumentPath: models.BuildPath(r.Category, r.Subcategory, r.Slug),
				Message:      fmt.Sprintf("access-cold eviction candidate: unaccessed for %d days (would be archived once access_retention_enabled is on)", r.DaysCold),
			})
		}
	}
	return findings, nil
}

// CheckSparse finds documents with too few sections or insufficient content length.
func (r *LintRepository) CheckSparse(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT d.category, d.subcategory, d.slug,
		       COUNT(s.id) AS section_count,
		       COALESCE(MAX(LENGTH(s.content)), 0) AS max_content_len
		FROM documents d
		LEFT JOIN sections s ON s.document_id = d.id
		WHERE d.tenant_id IN ?
		  AND d.archived_at IS NULL
		GROUP BY d.id, d.category, d.subcategory, d.slug
		HAVING COUNT(s.id) < ? OR COALESCE(MAX(LENGTH(s.content)), 0) < ?
		ORDER BY d.category, d.subcategory, d.slug
	`

	type row struct {
		Category      string  `gorm:"column:category"`
		Subcategory   *string `gorm:"column:subcategory"`
		Slug          string  `gorm:"column:slug"`
		SectionCount  int     `gorm:"column:section_count"`
		MaxContentLen int     `gorm:"column:max_content_len"`
	}

	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, thresholds.SparseMinSections, thresholds.SparseMinContentLen).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check sparse: %w", err)
	}

	findings := make([]LintFinding, 0, len(rows))
	for _, r := range rows {
		findings = append(findings, LintFinding{
			Check:        "sparse",
			Severity:     LintSeverityInfo,
			DocumentPath: models.BuildPath(r.Category, r.Subcategory, r.Slug),
			Message:      fmt.Sprintf("document has %d sections and max content length %d (thresholds: min_sections=%d, min_content_len=%d)", r.SectionCount, r.MaxContentLen, thresholds.SparseMinSections, thresholds.SparseMinContentLen),
		})
	}
	return findings, nil
}

// NearDuplicatePair is a raw doc pair from the near-duplicate scan. The
// document IDs let the cleanup scanner upsert into the cleanup_queue table.
type NearDuplicatePair struct {
	DocAID     uuid.UUID `gorm:"column:doc_a_id"`
	DocBID     uuid.UUID `gorm:"column:doc_b_id"`
	Similarity float64   `gorm:"column:similarity"`
}

// FindNearDuplicatePairs returns doc-ID pairs whose section-level cosine reaches
// the threshold. Metric: MAX(1 - cosine) over section pairs — "best matching
// section across the pair", more discriminating than doc-AVG. Feeds the cleanup
// queue.
//
// Cost control (audit #14): scoped to the tenant's OWN docs only — the shared
// common pool is excluded, so cross-tenant override pairs (never auto-mergeable)
// aren't enqueued. Bounded via per-section HNSW k-NN, capped outer scan and result
// set, under a statement_timeout (runBoundedScan).
func (r *LintRepository) FindNearDuplicatePairs(ctx context.Context, tenantID uuid.UUID, threshold float64) ([]NearDuplicatePair, error) {
	maxSections, neighbors, maxPairs := DefaultLintThresholds().dupScanCaps()
	episodic := pq.StringArray(models.EpisodicDocTypes())

	sql := `
		WITH cand AS (
			SELECT s.document_id, s.embedding
			FROM documents d
			JOIN sections s ON s.document_id = d.id
			WHERE d.tenant_id = ?
			  AND d.archived_at IS NULL
			  AND d.doc_type <> ALL(?)
			  AND s.embedding IS NOT NULL
			LIMIT ?
		)
		SELECT LEAST(c.document_id, nn.document_id) AS doc_a_id,
		       GREATEST(c.document_id, nn.document_id) AS doc_b_id,
		       MAX(nn.similarity) AS similarity
		FROM cand c
		CROSS JOIN LATERAL (
			SELECT s2.document_id, 1 - (s2.embedding <=> c.embedding) AS similarity
			FROM documents d2
			JOIN sections s2 ON s2.document_id = d2.id
			WHERE d2.tenant_id = ?
			  AND d2.archived_at IS NULL
			  AND d2.doc_type <> ALL(?)
			  AND s2.embedding IS NOT NULL
			  AND s2.document_id <> c.document_id
			ORDER BY s2.embedding <=> c.embedding
			LIMIT ?
		) nn
		GROUP BY LEAST(c.document_id, nn.document_id), GREATEST(c.document_id, nn.document_id)
		HAVING MAX(nn.similarity) >= ?
		ORDER BY similarity DESC
		LIMIT ?
	`
	var pairs []NearDuplicatePair
	if err := r.runBoundedScan(ctx, &pairs, sql,
		tenantID, episodic, maxSections, tenantID, episodic, neighbors, threshold, maxPairs); err != nil {
		return nil, fmt.Errorf("find near duplicate pairs: %w", err)
	}
	return pairs, nil
}

// CheckNearDuplicates finds doc pairs sharing at least one near-duplicate section.
// Metric: MAX section-pair cosine per pair, above thresholds.DuplicateSimilarity.
func (r *LintRepository) CheckNearDuplicates(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)
	maxSections, neighbors, maxPairs := thresholds.dupScanCaps()
	episodic := pq.StringArray(models.EpisodicDocTypes())

	// Cost control (audit #10): user-triggerable, so the exact O(N^2) cross join is
	// replaced by a bounded per-section HNSW k-NN probe under a statement_timeout
	// (runBoundedScan). Semantics preserved: same MAX-cosine metric, tenant+common
	// read scope, strict `>` threshold.
	sql := `
		WITH cand AS (
			SELECT s.document_id, s.embedding
			FROM documents d
			JOIN sections s ON s.document_id = d.id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND d.doc_type <> ALL(?)
			  AND s.embedding IS NOT NULL
			LIMIT ?
		),
		pairs AS (
			SELECT LEAST(c.document_id, nn.document_id) AS doc_a_id,
			       GREATEST(c.document_id, nn.document_id) AS doc_b_id,
			       MAX(nn.similarity) AS similarity
			FROM cand c
			CROSS JOIN LATERAL (
				SELECT s2.document_id, 1 - (s2.embedding <=> c.embedding) AS similarity
				FROM documents d2
				JOIN sections s2 ON s2.document_id = d2.id
				WHERE d2.tenant_id IN ?
				  AND d2.archived_at IS NULL
				  AND d2.doc_type <> ALL(?)
				  AND s2.embedding IS NOT NULL
				  AND s2.document_id <> c.document_id
				ORDER BY s2.embedding <=> c.embedding
				LIMIT ?
			) nn
			GROUP BY LEAST(c.document_id, nn.document_id), GREATEST(c.document_id, nn.document_id)
			HAVING MAX(nn.similarity) > ?
			ORDER BY similarity DESC
			LIMIT ?
		)
		SELECT da.category AS cat1, da.subcategory AS sub1, da.slug AS slug1,
		       db.category AS cat2, db.subcategory AS sub2, db.slug AS slug2,
		       p.similarity
		FROM pairs p
		JOIN documents da ON da.id = p.doc_a_id
		JOIN documents db ON db.id = p.doc_b_id
		ORDER BY p.similarity DESC
	`

	type row struct {
		Cat1       string  `gorm:"column:cat1"`
		Sub1       *string `gorm:"column:sub1"`
		Slug1      string  `gorm:"column:slug1"`
		Cat2       string  `gorm:"column:cat2"`
		Sub2       *string `gorm:"column:sub2"`
		Slug2      string  `gorm:"column:slug2"`
		Similarity float64 `gorm:"column:similarity"`
	}

	var rows []row
	if err := r.runBoundedScan(ctx, &rows, sql,
		tenants, episodic, maxSections, tenants, episodic, neighbors, thresholds.DuplicateSimilarity, maxPairs); err != nil {
		return nil, fmt.Errorf("check near duplicates: %w", err)
	}

	findings := make([]LintFinding, 0, len(rows))
	for _, r := range rows {
		path1 := models.BuildPath(r.Cat1, r.Sub1, r.Slug1)
		path2 := models.BuildPath(r.Cat2, r.Sub2, r.Slug2)
		findings = append(findings, LintFinding{
			Check:        "near_duplicate",
			Severity:     LintSeverityWarning,
			DocumentPath: path1,
			Message:      fmt.Sprintf("document is %.2f%% similar to %s (threshold: %.2f%%)", r.Similarity*100, path2, thresholds.DuplicateSimilarity*100),
		})
	}
	return findings, nil
}

// CheckEmptyCategories finds subcategories with fewer documents than the minimum threshold.
func (r *LintRepository) CheckEmptyCategories(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT category, subcategory, COUNT(*) AS doc_count
		FROM documents
		WHERE tenant_id IN ?
		  AND archived_at IS NULL
		GROUP BY category, subcategory
		HAVING COUNT(*) < ?
		ORDER BY category, subcategory
	`

	type row struct {
		Category    string  `gorm:"column:category"`
		Subcategory *string `gorm:"column:subcategory"`
		DocCount    int     `gorm:"column:doc_count"`
	}

	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, thresholds.EmptyCategoryMinDocs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check empty categories: %w", err)
	}

	findings := make([]LintFinding, 0, len(rows))
	for _, r := range rows {
		var path string
		if r.Subcategory != nil {
			path = r.Category + "/" + *r.Subcategory
		} else {
			path = r.Category
		}
		findings = append(findings, LintFinding{
			Check:        "empty_category",
			Severity:     LintSeverityInfo,
			DocumentPath: path,
			Message:      fmt.Sprintf("category has only %d document(s) (threshold: %d)", r.DocCount, thresholds.EmptyCategoryMinDocs),
		})
	}
	return findings, nil
}
