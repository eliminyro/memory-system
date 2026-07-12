package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// SimilarityCandidate is an existing document that may collide with a new save.
// MaxSimilarity is the cosine (0..1) of the best-matching section pair between
// the new embeddings and the candidate's existing sections.
type SimilarityCandidate struct {
	DocumentID    uuid.UUID `json:"document_id"`
	Category      string    `json:"category"`
	Subcategory   *string   `json:"subcategory,omitempty"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	MaxSimilarity float64   `json:"similarity"`
}

// Path returns the candidate's hierarchical path string.
func (c SimilarityCandidate) Path() string {
	if c.Subcategory != nil {
		return c.Category + "/" + *c.Subcategory + "/" + c.Slug
	}
	return c.Category + "/" + c.Slug
}

// FindSimilarDocuments scans the tenant's existing sections and returns documents
// whose best section match against any of newEmbeddings meets the threshold. Pass
// excludeCategory/Subcategory/Slug so updating an existing doc doesn't self-flag.
// Returns up to `limit`, ordered by similarity desc. Runs as one query via a
// CROSS JOIN over a Postgres vector array literal.
func (r *SectionRepository) FindSimilarDocuments(
	ctx context.Context,
	tenantID uuid.UUID,
	newEmbeddings []pgvector.Vector,
	threshold float64,
	limit int,
	excludeCategory string,
	excludeSubcategory *string,
	excludeSlug string,
) ([]SimilarityCandidate, error) {
	if len(newEmbeddings) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	tenants := readTenants(tenantID)
	arrayLit := buildVectorArrayLiteral(newEmbeddings)

	sql := `
		SELECT s.document_id,
		       d.category, d.subcategory, d.slug, d.title,
		       MAX(1 - (s.embedding <=> nv.vec)) AS max_similarity
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		CROSS JOIN unnest(?::vector[]) AS nv(vec)
		WHERE d.tenant_id IN ?
		  AND d.archived_at IS NULL
		  AND NOT (d.category = ? AND COALESCE(d.subcategory, '') = COALESCE(?, '') AND d.slug = ?)
		  AND s.embedding IS NOT NULL
		GROUP BY s.document_id, d.category, d.subcategory, d.slug, d.title
		HAVING MAX(1 - (s.embedding <=> nv.vec)) >= ?
		ORDER BY max_similarity DESC
		LIMIT ?
	`
	args := []any{arrayLit, tenants, excludeCategory, excludeSubcategory, excludeSlug, threshold, limit}

	var results []SimilarityCandidate
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("find similar documents: %w", err)
	}
	return results, nil
}

// buildVectorArrayLiteral renders []pgvector.Vector as a Postgres array literal for
// `?::vector[]`: `{"[1,2,3]","[4,5,6]"}`. Each element is quoted so its internal
// commas aren't parsed as array separators.
func buildVectorArrayLiteral(vectors []pgvector.Vector) string {
	parts := make([]string, len(vectors))
	for i, v := range vectors {
		parts[i] = `"` + v.String() + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}
