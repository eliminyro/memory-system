package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// SimilarityCandidate is an existing document that may collide with a new save.
// MaxSimilarity is the cosine similarity (0..1) of the best-matching section
// across any of the new section embeddings compared to any of the candidate's
// existing section embeddings.
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

// FindSimilarDocuments scans existing sections in the tenant and returns
// documents whose best section match against any of `newEmbeddings` meets
// the threshold. Pass excludeCategory/Subcategory/Slug to avoid matching
// the same path (so updating an existing doc doesn't flag as duplicate).
//
// Returns up to `limit` candidates, ordered by similarity descending.
//
// Runs one query per embedding and aggregates in Go. Fine for typical saves
// (a handful of sections per doc); we avoid pgvector array literal gymnastics
// by keeping the SQL scalar-parameterised.
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

	sql := `
		SELECT s.document_id,
		       d.category, d.subcategory, d.slug, d.title,
		       MAX(1 - (s.embedding <=> ?::vector)) AS max_similarity
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		WHERE d.tenant_id IN ?
		  AND NOT (d.category = ? AND COALESCE(d.subcategory, '') = COALESCE(?, '') AND d.slug = ?)
		  AND s.embedding IS NOT NULL
		GROUP BY s.document_id, d.category, d.subcategory, d.slug, d.title
		HAVING MAX(1 - (s.embedding <=> ?::vector)) >= ?
		ORDER BY max_similarity DESC
		LIMIT ?
	`

	// Aggregate best-per-doc across all new embeddings.
	best := make(map[uuid.UUID]SimilarityCandidate)
	perQueryLimit := limit * 3 // headroom so later embeddings can raise an earlier doc's max
	for _, emb := range newEmbeddings {
		vec := emb.String()
		args := []any{vec, tenants, excludeCategory, excludeSubcategory, excludeSlug, vec, threshold, perQueryLimit}

		var rows []SimilarityCandidate
		if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("find similar documents: %w", err)
		}
		for _, row := range rows {
			if existing, ok := best[row.DocumentID]; !ok || row.MaxSimilarity > existing.MaxSimilarity {
				best[row.DocumentID] = row
			}
		}
	}

	out := make([]SimilarityCandidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	// Sort descending by similarity, truncate to limit.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].MaxSimilarity > out[i].MaxSimilarity {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
