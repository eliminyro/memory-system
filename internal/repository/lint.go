package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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
}

func DefaultLintThresholds() LintThresholds {
	return LintThresholds{
		StaleDays:            90,
		SparseMinSections:    2,
		SparseMinContentLen:  100,
		DuplicateSimilarity:  0.92,
		EmptyCategoryMinDocs: 2,
	}
}

// CheckStale finds documents that have not been updated within the stale_days threshold.
func (r *LintRepository) CheckStale(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT category, subcategory, slug, title, updated_at,
		       EXTRACT(DAY FROM NOW() - updated_at)::int AS days_since_update
		FROM documents
		WHERE tenant_id IN ?
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
	if err := r.db.WithContext(ctx).Raw(sql, tenants, thresholds.StaleDays).Scan(&rows).Error; err != nil {
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

// FindNearDuplicatePairs returns doc ID pairs whose average embeddings meet
// the threshold. Same logic as CheckNearDuplicates but returns IDs instead of
// human-readable findings — used by the cleanup pipeline to populate the queue.
func (r *LintRepository) FindNearDuplicatePairs(ctx context.Context, tenantID uuid.UUID, threshold float64) ([]NearDuplicatePair, error) {
	tenants := readTenants(tenantID)

	sql := `
		WITH doc_avg AS (
			SELECT d.id, AVG(s.embedding) AS avg_embedding
			FROM documents d
			JOIN sections s ON s.document_id = d.id
			WHERE d.tenant_id IN ?
			  AND s.embedding IS NOT NULL
			GROUP BY d.id
		)
		SELECT a.id AS doc_a_id, b.id AS doc_b_id,
		       1 - (a.avg_embedding <=> b.avg_embedding) AS similarity
		FROM doc_avg a
		JOIN doc_avg b ON a.id < b.id
		WHERE 1 - (a.avg_embedding <=> b.avg_embedding) >= ?
		ORDER BY similarity DESC
	`
	var pairs []NearDuplicatePair
	if err := r.db.WithContext(ctx).Raw(sql, tenants, threshold).Scan(&pairs).Error; err != nil {
		return nil, fmt.Errorf("find near duplicate pairs: %w", err)
	}
	return pairs, nil
}

// CheckNearDuplicates finds pairs of documents whose average embeddings are highly similar.
func (r *LintRepository) CheckNearDuplicates(ctx context.Context, tenantID uuid.UUID, thresholds LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		WITH doc_avg AS (
			SELECT d.id, d.category, d.subcategory, d.slug,
			       AVG(s.embedding) AS avg_embedding
			FROM documents d
			JOIN sections s ON s.document_id = d.id
			WHERE d.tenant_id IN ?
			  AND s.embedding IS NOT NULL
			GROUP BY d.id, d.category, d.subcategory, d.slug
		)
		SELECT a.category AS cat1, a.subcategory AS sub1, a.slug AS slug1,
		       b.category AS cat2, b.subcategory AS sub2, b.slug AS slug2,
		       1 - (a.avg_embedding <=> b.avg_embedding) AS similarity
		FROM doc_avg a
		JOIN doc_avg b ON a.id < b.id
		WHERE 1 - (a.avg_embedding <=> b.avg_embedding) > ?
		ORDER BY similarity DESC
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
	if err := r.db.WithContext(ctx).Raw(sql, tenants, thresholds.DuplicateSimilarity).Scan(&rows).Error; err != nil {
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
