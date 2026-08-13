package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/repository"
)

// candidatePoolLimit mirrors internal/repository/section.go semantic/keyword
// CTE LIMIT as of 7940fd5 — the candidate-pool depth hybrid fuses from,
// distinct from maxK (the scoring depth all three modes report at).
const candidatePoolLimit = 20

// HybridRetrieve runs the shipped hybrid search (task 3.1), scoped to one
// question's bench subcategory via Category/Subcategory. section.go is
// untouched; results are already rank-ordered by fuseHybrid's score sort.
func HybridRetrieve(ctx context.Context, sections *repository.SectionRepository, tenantID uuid.UUID, subcategory string, embedding pgvector.Vector, query string, maxK int) ([]RetrievedSection, error) {
	category := benchCategory
	results, err := sections.HybridSearch(ctx, repository.SearchParams{
		TenantIDs:   []uuid.UUID{tenantID},
		Embedding:   embedding,
		Query:       query,
		Category:    &category,
		Subcategory: &subcategory,
		Limit:       maxK,
	})
	if err != nil {
		return nil, fmt.Errorf("hybrid retrieve: %w", err)
	}

	out := make([]RetrievedSection, len(results))
	for i, r := range results {
		out[i] = RetrievedSection{SessionID: r.Slug}
	}
	return out, nil
}

// mirrorRow is the scan target for the vector-only/lexical-only mirror
// queries below — only the owning document's slug is needed.
type mirrorRow struct {
	Slug string `gorm:"column:slug"`
}

// VectorOnlyRetrieve (task 3.2) mirrors internal/repository/section.go's
// `semantic` CTE as of 7940fd5: same tenant/category/subcategory/archived
// scoping and the same join to documents for the slug, but LIMIT maxK
// directly since there's no fusion step downstream of this arm.
func VectorOnlyRetrieve(ctx context.Context, db *gorm.DB, tenantID uuid.UUID, subcategory string, embedding pgvector.Vector, maxK int) ([]RetrievedSection, error) {
	category := benchCategory
	sql := `
		SELECT d.slug
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		WHERE d.tenant_id IN ?
		  AND d.archived_at IS NULL
		  AND (?::text IS NULL OR d.category = ?)
		  AND (?::text IS NULL OR d.subcategory = ?)
		  AND s.embedding IS NOT NULL
		ORDER BY s.embedding <=> ?::vector
		LIMIT ?
	`
	args := []any{[]uuid.UUID{tenantID}, category, category, subcategory, subcategory, embedding.String(), maxK}

	var rows []mirrorRow
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("vector-only retrieve: %w", err)
	}
	return toRetrieved(rows), nil
}

// LexicalOnlyRetrieve (task 3.2) mirrors internal/repository/section.go's
// `keyword` CTE as of 7940fd5: same tenant/category/subcategory/archived
// scoping and the same join to documents for the slug, but LIMIT maxK
// directly since there's no fusion step downstream of this arm.
func LexicalOnlyRetrieve(ctx context.Context, db *gorm.DB, tenantID uuid.UUID, subcategory, query string, maxK int) ([]RetrievedSection, error) {
	category := benchCategory
	sql := `
		SELECT d.slug
		FROM sections s
		JOIN documents d ON d.id = s.document_id
		WHERE d.tenant_id IN ?
		  AND d.archived_at IS NULL
		  AND (?::text IS NULL OR d.category = ?)
		  AND (?::text IS NULL OR d.subcategory = ?)
		  AND s.tsv @@ plainto_tsquery('english', ?)
		ORDER BY ts_rank(s.tsv, plainto_tsquery('english', ?)) DESC
		LIMIT ?
	`
	args := []any{[]uuid.UUID{tenantID}, category, category, subcategory, subcategory, query, query, maxK}

	var rows []mirrorRow
	if err := db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("lexical-only retrieve: %w", err)
	}
	return toRetrieved(rows), nil
}

func toRetrieved(rows []mirrorRow) []RetrievedSection {
	out := make([]RetrievedSection, len(rows))
	for i, r := range rows {
		out[i] = RetrievedSection{SessionID: r.Slug}
	}
	return out
}

// checkMirrorDrift re-pulls vector-only/lexical-only at candidatePoolLimit —
// the real CTEs' candidate-pool depth, NOT maxK — since hybrid ⊆ vector ∪
// lexical only holds when the mirrors are pulled at that depth (a session
// ranked 11-20 can land in hybrid's top-maxK while missing from a maxK-deep
// mirror). Warn-only diagnostic: errors and drift are both just logged.
func checkMirrorDrift(ctx context.Context, db *gorm.DB, tenantID uuid.UUID, subcategory, questionID string, embedding pgvector.Vector, query string, hybrid []RetrievedSection) {
	vectorPool, err := VectorOnlyRetrieve(ctx, db, tenantID, subcategory, embedding, candidatePoolLimit)
	if err != nil {
		slog.Warn("drift check: vector-only pool retrieve failed", "question_id", questionID, "error", err)
		return
	}
	lexicalPool, err := LexicalOnlyRetrieve(ctx, db, tenantID, subcategory, query, candidatePoolLimit)
	if err != nil {
		slog.Warn("drift check: lexical-only pool retrieve failed", "question_id", questionID, "error", err)
		return
	}
	if missing := CheckDrift(hybrid, vectorPool, lexicalPool); len(missing) > 0 {
		slog.Warn("mirror drift detected: hybrid session not in vector-only ∪ lexical-only candidate pool",
			"question_id", questionID, "sessions", missing)
	}
}

// CheckDrift guards against the mirror queries above drifting from
// section.go's real CTEs (design risk "Mirror drift"): hybrid fuses exactly
// the vector-only and lexical-only candidate pools, so every session hybrid
// returns must appear in one of them. Returns the offending session ids
// (empty = no drift); pure function, usable in a unit test or at runtime
// against real retrieval results. Callers MUST pass vectorOnly/lexicalOnly
// pulled at candidatePoolLimit depth (see checkMirrorDrift), not maxK.
func CheckDrift(hybrid, vectorOnly, lexicalOnly []RetrievedSection) []string {
	union := make(map[string]struct{}, len(vectorOnly)+len(lexicalOnly))
	for _, r := range vectorOnly {
		union[r.SessionID] = struct{}{}
	}
	for _, r := range lexicalOnly {
		union[r.SessionID] = struct{}{}
	}

	seen := make(map[string]struct{}, len(hybrid))
	var missing []string
	for _, r := range hybrid {
		if _, ok := seen[r.SessionID]; ok {
			continue
		}
		seen[r.SessionID] = struct{}{}
		if _, ok := union[r.SessionID]; !ok {
			missing = append(missing, r.SessionID)
		}
	}
	return missing
}
