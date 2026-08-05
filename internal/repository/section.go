package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

type SectionRepository struct {
	db *gorm.DB
}

func NewSectionRepository(db *gorm.DB) *SectionRepository {
	return &SectionRepository{db: db}
}

type SearchResult struct {
	SectionID      uuid.UUID  `json:"section_id"`
	DocumentID     uuid.UUID  `json:"document_id"`
	Heading        *string    `json:"heading,omitempty"`
	Content        string     `json:"content,omitempty"`
	Score          float64    `json:"score"`
	Tier           string     `json:"relevance,omitempty"` // high | standard | low — calibrated from Score
	Category       string     `json:"category"`
	Subcategory    *string    `json:"subcategory,omitempty"`
	Slug           string     `json:"slug"`
	DocTitle       string     `json:"doc_title"`
	DocType        string     `json:"doc_type,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	SectionCreated time.Time  `json:"-"`

	// Owning-tenant label (cross-tenant reads). TenantID comes from SQL; Name and
	// Type are resolved by the service layer for the distinct result tenants.
	TenantID   uuid.UUID `json:"tenant_id"`
	TenantName string    `json:"tenant_name,omitempty"`
	TenantType string    `json:"tenant_type,omitempty"`

	// Staleness overlay (set by service layer after fetch, not by SQL).
	Status        string   `json:"status,omitempty"`         // "needs_verification" when guarded
	Preview       string   `json:"preview,omitempty"`        // short preview of withheld content
	VerifyHints   []string `json:"verify_hints,omitempty"`   // code paths to check
	StaleDays     int      `json:"age_days,omitempty"`       // age of verified_at in days
	ThresholdDays int      `json:"threshold_days,omitempty"` // threshold for this doc_type
}

// SearchParams groups the inputs for hybrid search.
type SearchParams struct {
	TenantIDs   []uuid.UUID
	Embedding   pgvector.Vector
	Query       string
	Category    *string
	Subcategory *string
	Limit       int
}

// Hybrid retrieval fusion constants. Lexical is weighted higher than vector: a row
// matching BOTH is genuinely relevant, while weak vector-only matches are noise.
const (
	fuseVecWeight = 0.4 // weight of vector similarity when both signals fire
	fuseLexWeight = 0.6 // weight of lexical rank when both signals fire
	scoreFloor    = 0.4 // drop fused scores below this (weak-match noise)
	tierHighMin   = 0.7 // >= this is "high"
	tierStdMin    = 0.5 // >= this is "standard"; below is "low"
)

// hybridRow is a raw candidate from the FULL OUTER JOIN (vector, lexical, or both).
// Fusion happens in Go (fuseHybrid) so it stays pure and unit-testable.
type hybridRow struct {
	SectionID      uuid.UUID  `gorm:"column:section_id"`
	DocumentID     uuid.UUID  `gorm:"column:document_id"`
	Heading        *string    `gorm:"column:heading"`
	Content        string     `gorm:"column:content"`
	VecSim         float64    `gorm:"column:vec_sim"`
	LexRank        float64    `gorm:"column:lex_rank"`
	HasVec         bool       `gorm:"column:has_vec"`
	HasLex         bool       `gorm:"column:has_lex"`
	Category       string     `gorm:"column:category"`
	Subcategory    *string    `gorm:"column:subcategory"`
	Slug           string     `gorm:"column:slug"`
	DocTitle       string     `gorm:"column:doc_title"`
	DocType        string     `gorm:"column:doc_type"`
	TenantID       uuid.UUID  `gorm:"column:tenant_id"`
	VerifiedAt     *time.Time `gorm:"column:verified_at"`
	SectionCreated time.Time  `gorm:"column:section_created"`
}

// relevanceTier calibrates a fused score into a label the model can act on.
func relevanceTier(score float64) string {
	switch {
	case score >= tierHighMin:
		return "high"
	case score >= tierStdMin:
		return "standard"
	default:
		return "low"
	}
}

// fuseHybrid normalizes lexical ranks to the batch max, fuses with vector
// similarity, drops sub-floor results, tiers, sorts desc, and caps to limit.
// Lexical-only matches are kept (the point of the FULL OUTER JOIN) but weighted by
// lexical weight alone, so they top out at "standard" tier.
func fuseHybrid(rows []hybridRow, limit int) []SearchResult {
	var maxLex float64
	for _, r := range rows {
		if r.HasLex && r.LexRank > maxLex {
			maxLex = r.LexRank
		}
	}

	out := make([]SearchResult, 0, len(rows))
	for _, r := range rows {
		vec := r.VecSim
		if vec < 0 {
			vec = 0 // cosine similarity can go negative; clamp.
		}
		var lex float64
		if r.HasLex && maxLex > 0 {
			lex = r.LexRank / maxLex
		}

		var score float64
		switch {
		case r.HasVec && r.HasLex:
			score = fuseVecWeight*vec + fuseLexWeight*lex
		case r.HasVec:
			score = vec
		default: // lexical-only
			score = fuseLexWeight * lex
		}
		if score < scoreFloor {
			continue
		}

		out = append(out, SearchResult{
			SectionID:      r.SectionID,
			DocumentID:     r.DocumentID,
			Heading:        r.Heading,
			Content:        r.Content,
			Score:          score,
			Tier:           relevanceTier(score),
			Category:       r.Category,
			Subcategory:    r.Subcategory,
			Slug:           r.Slug,
			DocTitle:       r.DocTitle,
			DocType:        r.DocType,
			TenantID:       r.TenantID,
			VerifiedAt:     r.VerifiedAt,
			SectionCreated: r.SectionCreated,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// HybridSearch gathers vector and lexical candidates (scope-filtered, capped) via
// a FULL OUTER JOIN so lexical-only matches are recalled, then fuses them in Go.
// Scope filters run inside both CTEs, before ranking.
func (r *SectionRepository) HybridSearch(ctx context.Context, p SearchParams) ([]SearchResult, error) {
	if p.Limit <= 0 {
		p.Limit = 10
	}

	tenants := p.TenantIDs
	vec := p.Embedding.String()

	sql := `
		WITH semantic AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created,
				   1 - (s.embedding <=> ?::vector) AS vec_sim
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND s.embedding IS NOT NULL
			ORDER BY s.embedding <=> ?::vector
			LIMIT 20
		),
		keyword AS (
			SELECT s.id, s.document_id, s.heading, s.content, s.verified_at,
				   s.created_at AS section_created,
				   ts_rank(s.tsv, plainto_tsquery('english', ?)) AS lex_rank
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND s.tsv @@ plainto_tsquery('english', ?)
			ORDER BY lex_rank DESC
			LIMIT 20
		)
		SELECT COALESCE(sem.id, kw.id)                       AS section_id,
			   COALESCE(sem.document_id, kw.document_id)     AS document_id,
			   COALESCE(sem.heading, kw.heading)             AS heading,
			   COALESCE(sem.content, kw.content)             AS content,
			   COALESCE(sem.vec_sim, 0)                      AS vec_sim,
			   COALESCE(kw.lex_rank, 0)                      AS lex_rank,
			   (sem.id IS NOT NULL)                          AS has_vec,
			   (kw.id IS NOT NULL)                           AS has_lex,
			   d.category, d.subcategory, d.slug, d.title    AS doc_title,
			   d.doc_type, d.tenant_id,
			   COALESCE(sem.verified_at, kw.verified_at)     AS verified_at,
			   COALESCE(sem.section_created, kw.section_created) AS section_created
		FROM semantic sem
		FULL OUTER JOIN keyword kw ON kw.id = sem.id
		JOIN documents d ON d.id = COALESCE(sem.document_id, kw.document_id)
	`

	args := []any{
		vec, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, vec, // semantic
		p.Query, tenants, p.Category, p.Category, p.Subcategory, p.Subcategory, p.Query, // keyword
	}

	var rows []hybridRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	return fuseHybrid(rows, p.Limit), nil
}

type RelatedResult struct {
	DocumentID  uuid.UUID `json:"document_id"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	Slug        string    `json:"slug"`
	DocTitle    string    `json:"doc_title"`
	Similarity  float64   `json:"similarity"`
}

func (r *SectionRepository) GetRelated(ctx context.Context, tenantID uuid.UUID, documentID uuid.UUID, limit int) ([]RelatedResult, error) {
	if limit <= 0 {
		limit = 5
	}

	tenants := readTenants(tenantID)

	sql := `
		WITH target_avg AS (
			SELECT AVG(embedding) AS avg_embedding
			FROM sections
			WHERE document_id = ?
			  AND embedding IS NOT NULL
		),
		candidates AS (
			SELECT s.document_id,
				   1 - (s.embedding <=> (SELECT avg_embedding FROM target_avg)) AS similarity
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE s.document_id != ?
			  AND d.tenant_id IN ?
			  AND d.archived_at IS NULL
			  AND s.embedding IS NOT NULL
		)
		SELECT c.document_id, d.category, d.subcategory, d.slug, d.title AS doc_title,
			   AVG(c.similarity) AS similarity
		FROM candidates c
		JOIN documents d ON d.id = c.document_id
		GROUP BY c.document_id, d.category, d.subcategory, d.slug, d.title
		ORDER BY similarity DESC
		LIMIT ?
	`

	args := []any{documentID, documentID, tenants, limit}

	var results []RelatedResult
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("get related: %w", err)
	}
	return results, nil
}

func (r *SectionRepository) DeleteByDocumentID(ctx context.Context, docID uuid.UUID) error {
	return r.db.WithContext(ctx).Where("document_id = ?", docID).Delete(&models.Section{}).Error
}

func (r *SectionRepository) CreateBatch(ctx context.Context, sections []models.Section) error {
	if len(sections) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&sections).Error
}

func (r *SectionRepository) Update(ctx context.Context, section *models.Section) error {
	return r.db.WithContext(ctx).Save(section).Error
}

func (r *SectionRepository) GetByID(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) (*models.Section, error) {
	var section models.Section
	if err := r.db.WithContext(ctx).
		Joins("JOIN documents d ON d.id = sections.document_id").
		Where("d.tenant_id IN ?", readTenants(tenantID)).
		Preload("Document").
		First(&section, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: section %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &section, nil
}

// MarkVerified sets verified_at = NOW() if the section belongs to one of the
// caller's accessible tenants. ErrNotFound if not in scope.
func (r *SectionRepository) MarkVerified(ctx context.Context, tenantID uuid.UUID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&models.Section{}).
		Where(`id = ? AND document_id IN (SELECT id FROM documents WHERE tenant_id IN ?)`, id, readTenants(tenantID)).
		Update("verified_at", gorm.Expr("NOW()"))
	if result.Error != nil {
		return fmt.Errorf("mark verified: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: section %s", apperr.ErrNotFound, id)
	}
	return nil
}
