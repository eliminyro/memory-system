package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/models"
)

type SectionRepository struct {
	db *gorm.DB
}

func NewSectionRepository(db *gorm.DB) *SectionRepository {
	return &SectionRepository{db: db}
}

type SearchResult struct {
	SectionID   uuid.UUID `json:"section_id"`
	DocumentID  uuid.UUID `json:"document_id"`
	Heading     *string   `json:"heading,omitempty"`
	Content     string    `json:"content"`
	Score       float64   `json:"score"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	Slug        string    `json:"slug"`
	DocTitle    string    `json:"doc_title"`
}

func (r *SectionRepository) HybridSearch(ctx context.Context, embedding pgvector.Vector, query string, category *string, subcategory *string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	sql := `
		WITH semantic AS (
			SELECT s.id, s.document_id, s.heading, s.content,
				   1 - (s.embedding <=> ?::vector) AS score
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE (?::text IS NULL OR d.category = ?)
			  AND (?::text IS NULL OR d.subcategory = ?)
			  AND s.embedding IS NOT NULL
			ORDER BY s.embedding <=> ?::vector
			LIMIT 20
		),
		keyword AS (
			SELECT s.id, ts_rank(s.tsv, plainto_tsquery('english', ?)) AS score
			FROM sections s
			WHERE s.tsv @@ plainto_tsquery('english', ?)
		)
		SELECT sem.id AS section_id, sem.document_id, sem.heading, sem.content,
			   (0.7 * sem.score + 0.3 * COALESCE(kw.score, 0)) AS score,
			   d.category, d.subcategory, d.slug, d.title AS doc_title
		FROM semantic sem
		LEFT JOIN keyword kw ON kw.id = sem.id
		JOIN documents d ON d.id = sem.document_id
		ORDER BY score DESC
		LIMIT ?
	`

	vec := embedding.String()
	var results []SearchResult
	if err := r.db.WithContext(ctx).Raw(sql, vec, category, category, subcategory, subcategory, vec, query, query, limit).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
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

func (r *SectionRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Section, error) {
	var section models.Section
	if err := r.db.WithContext(ctx).Preload("Document").First(&section, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: section %s", apperr.ErrNotFound, id)
		}
		return nil, err
	}
	return &section, nil
}
