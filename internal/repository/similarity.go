package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// SimilarityCandidate is an existing document that may collide with a new save.
// Similarity is the cosine (0..1) between the new document's centroid and this
// candidate's centroid (mean of its sections); 1.0 on an exact-hash hit.
type SimilarityCandidate struct {
	DocumentID  uuid.UUID `json:"document_id"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Similarity  float64   `json:"similarity"`
}

// Path returns the candidate's hierarchical path string.
func (c SimilarityCandidate) Path() string {
	if c.Subcategory != nil {
		return c.Category + "/" + *c.Subcategory + "/" + c.Slug
	}
	return c.Category + "/" + c.Slug
}

// FindSimilarDocuments returns the write tenant's own documents whose centroid
// (AVG of section embeddings) meets threshold vs newCentroid — document-level, no
// section-count bias. Common pool excluded (un-editable), target path self-excluded.
func (r *SectionRepository) FindSimilarDocuments(
	ctx context.Context,
	tenantID uuid.UUID,
	newCentroid pgvector.Vector,
	threshold float64,
	limit int,
	excludeCategory string,
	excludeSubcategory *string,
	excludeSlug string,
) ([]SimilarityCandidate, error) {
	if limit <= 0 {
		limit = 5
	}

	// Write tenant only — never the common pool (see doc comment).
	tenants := []uuid.UUID{tenantID}

	sql := `
		SELECT s.document_id,
		       d.category, d.subcategory, d.slug, d.title,
		       1 - (AVG(s.embedding) <=> ?::vector) AS similarity
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		WHERE d.tenant_id IN ?
		  AND d.archived_at IS NULL
		  AND NOT (d.category = ? AND COALESCE(d.subcategory, '') = COALESCE(?, '') AND d.slug = ?)
		  AND s.embedding IS NOT NULL
		GROUP BY s.document_id, d.category, d.subcategory, d.slug, d.title
		HAVING (1 - (AVG(s.embedding) <=> ?::vector)) >= ?
		ORDER BY similarity DESC
		LIMIT ?
	`
	args := []any{newCentroid, tenants, excludeCategory, excludeSubcategory, excludeSlug, newCentroid, threshold, limit}

	var results []SimilarityCandidate
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("find similar documents: %w", err)
	}
	return results, nil
}

// FindByContentHash returns the write tenant's own non-excluded document with an
// identical content_hash, or (nil, nil) when none. Backs the exact-dup short-circuit;
// common pool excluded (tenant-scoped), target path self-excluded.
func (r *SectionRepository) FindByContentHash(
	ctx context.Context,
	tenantID uuid.UUID,
	hash string,
	excludeCategory string,
	excludeSubcategory *string,
	excludeSlug string,
) (*SimilarityCandidate, error) {
	sql := `
		SELECT id AS document_id, category, subcategory, slug, title
		FROM documents
		WHERE tenant_id = ?
		  AND content_hash = ?
		  AND archived_at IS NULL
		  AND NOT (category = ? AND COALESCE(subcategory, '') = COALESCE(?, '') AND slug = ?)
		LIMIT 1
	`
	args := []any{tenantID, hash, excludeCategory, excludeSubcategory, excludeSlug}

	var results []SimilarityCandidate
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("find by content hash: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return &results[0], nil
}
