package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

type MemoryService struct {
	db       *gorm.DB
	docs     *repository.DocumentRepository
	sections *repository.SectionRepository
	embedder *Embedder
}

func NewMemoryService(db *gorm.DB, docs *repository.DocumentRepository, sections *repository.SectionRepository, embedder *Embedder) *MemoryService {
	return &MemoryService{
		db:       db,
		docs:     docs,
		sections: sections,
		embedder: embedder,
	}
}

// Search performs hybrid semantic + keyword search.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int) ([]repository.SearchResult, error) {
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return s.sections.HybridSearch(ctx, embedding, query, category, subcategory, limit)
}

// GetDocument fetches a document with all sections by path.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string) (*models.Document, error) {
	return s.docs.GetByPath(ctx, category, subcategory, slug)
}

// StoreDocument parses markdown content into sections, embeds them, and stores.
// Embeddings are generated before any DB mutations to avoid partial state on failure.
func (s *MemoryService) StoreDocument(ctx context.Context, category string, subcategory *string, slug, content string) (*models.Document, error) {
	title, sections := parseMarkdown(content)
	if title == "" {
		title = slug
	}

	// Phase 1: Generate all embeddings BEFORE touching the database
	embeddings := make([]models.Section, len(sections))
	for i, sec := range sections {
		embedding, err := s.embedder.Embed(ctx, sec.content)
		if err != nil {
			return nil, fmt.Errorf("embed section %d: %w", i, err)
		}
		embeddings[i] = models.Section{
			Ordinal:   i,
			Heading:   sec.heading,
			Content:   sec.content,
			Embedding: embedding,
		}
	}

	// Phase 2: All DB mutations in a single transaction
	doc := &models.Document{
		Category:    category,
		Subcategory: subcategory,
		Slug:        slug,
		Title:       title,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Check for existing document
		existing, err := txDocs.GetByPath(ctx, category, subcategory, slug)
		if err == nil {
			// Update existing
			doc.ID = existing.ID
			doc.CreatedAt = existing.CreatedAt
			if err := txSections.DeleteByDocumentID(ctx, existing.ID); err != nil {
				return fmt.Errorf("delete old sections: %w", err)
			}
			if err := txDocs.Save(ctx, doc); err != nil {
				return fmt.Errorf("save document: %w", err)
			}
		} else {
			// Create new
			if err := txDocs.Create(ctx, doc); err != nil {
				return fmt.Errorf("create document: %w", err)
			}
		}

		// Set document ID on all sections and batch insert
		for i := range embeddings {
			embeddings[i].DocumentID = doc.ID
		}
		if err := txSections.CreateBatch(ctx, embeddings); err != nil {
			return fmt.Errorf("create sections: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	doc.Sections = embeddings
	return doc, nil
}

// UpdateSection updates a single section and re-embeds.
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content string) (*models.Section, error) {
	section, err := s.sections.GetByID(ctx, sectionID)
	if err != nil {
		return nil, fmt.Errorf("get section: %w", err)
	}

	embedding, err := s.embedder.Embed(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embed section: %w", err)
	}

	section.Content = content
	section.Embedding = embedding

	if err := s.sections.Update(ctx, section); err != nil {
		return nil, fmt.Errorf("update section: %w", err)
	}

	return section, nil
}

// DeleteDocument removes a document and all its sections.
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string) error {
	return s.docs.DeleteByPath(ctx, category, subcategory, slug)
}

// ListDocuments lists documents, optionally filtered.
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string) ([]models.Document, error) {
	return s.docs.List(ctx, category, subcategory)
}

// parsedSection holds a parsed markdown section.
type parsedSection struct {
	heading *string
	content string
}

// parseMarkdown splits markdown by ## headings into sections.
// Returns the title (from # heading) and sections.
func parseMarkdown(content string) (string, []parsedSection) {
	lines := strings.Split(content, "\n")
	var title string
	var sections []parsedSection
	var currentHeading *string
	var currentLines []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if text != "" {
			sections = append(sections, parsedSection{
				heading: currentHeading,
				content: text,
			})
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Extract title from # heading (first one only)
		if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ") && title == "" {
			title = strings.TrimPrefix(trimmed, "# ")
			continue
		}

		// Split on ## headings
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			h := strings.TrimPrefix(trimmed, "## ")
			currentHeading = &h
			continue
		}

		currentLines = append(currentLines, line)
	}

	flush()

	// If no sections were created (no ## headings), put everything in one section
	if len(sections) == 0 && strings.TrimSpace(content) != "" {
		sections = append(sections, parsedSection{
			heading: nil,
			content: strings.TrimSpace(content),
		})
	}

	return title, sections
}
