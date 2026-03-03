package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-mcp/internal/auth"
	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

type MemoryService struct {
	db       *gorm.DB
	docs     *repository.DocumentRepository
	sections *repository.SectionRepository
	embedder EmbeddingProvider
}

func NewMemoryService(db *gorm.DB, docs *repository.DocumentRepository, sections *repository.SectionRepository, embedder EmbeddingProvider) *MemoryService {
	return &MemoryService{
		db:       db,
		docs:     docs,
		sections: sections,
		embedder: embedder,
	}
}

// tenantFromCtx extracts the tenant ID from context, returning ErrInvalidInput if missing.
func tenantFromCtx(ctx context.Context) (uuid.UUID, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	return tid, nil
}

// Search performs hybrid semantic + keyword search.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int) ([]repository.SearchResult, error) {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return s.sections.HybridSearch(ctx, repository.SearchParams{
		TenantID:    tid,
		Embedding:   embedding,
		Query:       query,
		Category:    category,
		Subcategory: subcategory,
		Limit:       limit,
	})
}

// GetDocument fetches a document with all sections by path.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string) (*models.Document, error) {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	return s.docs.GetByPath(ctx, tid, category, subcategory, slug)
}

// StoreDocument parses markdown content into sections, embeds them, and stores.
// Embeddings are generated before any DB mutations to avoid partial state on failure.
func (s *MemoryService) StoreDocument(ctx context.Context, category string, subcategory *string, slug, content string) (*models.Document, error) {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	title, sections := parseMarkdown(content)
	if title == "" {
		title = slug
	}

	// Generate all embeddings BEFORE touching the database
	sectionModels := make([]models.Section, len(sections))
	for i, sec := range sections {
		embedding, err := s.embedder.Embed(ctx, sec.content)
		if err != nil {
			return nil, fmt.Errorf("embed section %d: %w", i, err)
		}
		sectionModels[i] = models.Section{
			Ordinal:   i,
			Heading:   sec.heading,
			Content:   sec.content,
			Embedding: embedding,
		}
	}

	// All DB mutations in a single transaction
	doc := &models.Document{
		TenantID:    tid,
		Category:    category,
		Subcategory: subcategory,
		Slug:        slug,
		Title:       title,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Check for existing document (only in this tenant, not common pool)
		existing, err := txDocs.GetByPath(ctx, tid, category, subcategory, slug)
		if err == nil && existing.TenantID == tid {
			// Update existing: delete sections first, then update document
			doc.ID = existing.ID
			doc.CreatedAt = existing.CreatedAt
			if err := txSections.DeleteByDocumentID(ctx, existing.ID); err != nil {
				return fmt.Errorf("delete old sections: %w", err)
			}
			if err := txDocs.Save(ctx, tid, doc); err != nil {
				return fmt.Errorf("save document: %w", err)
			}
		} else {
			// Create new
			if err := txDocs.Create(ctx, doc); err != nil {
				return fmt.Errorf("create document: %w", err)
			}
		}

		// Set document ID on all sections and batch insert
		for i := range sectionModels {
			sectionModels[i].DocumentID = doc.ID
		}
		if err := txSections.CreateBatch(ctx, sectionModels); err != nil {
			return fmt.Errorf("create sections: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	doc.Sections = sectionModels
	return doc, nil
}

// UpdateSection updates a single section and re-embeds.
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content string) (*models.Section, error) {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	section, err := s.sections.GetByID(ctx, tid, sectionID)
	if err != nil {
		return nil, fmt.Errorf("get section: %w", err)
	}

	// Prevent mutating common pool sections via regular MCP
	if section.Document != nil && section.Document.TenantID != tid {
		return nil, fmt.Errorf("%w: cannot update common pool section", apperr.ErrInvalidInput)
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

// DeleteDocument removes a document and all its sections in a transaction.
// Explicitly deletes sections first (FK-safe order), does not rely on CASCADE.
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string) error {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		// Look up the document (scoped to this tenant only for deletes)
		doc, err := txDocs.GetByPath(ctx, tid, category, subcategory, slug)
		if err != nil {
			return err
		}
		// Prevent deleting common pool docs via regular MCP
		if doc.TenantID != tid {
			return fmt.Errorf("%w: cannot delete common pool document", apperr.ErrInvalidInput)
		}

		// Delete sections first (FK-safe order)
		if err := txSections.DeleteByDocumentID(ctx, doc.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}

		// Then delete the document
		if err := txDocs.Delete(ctx, tid, doc.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}

		return nil
	})
}

// ListDocuments lists documents, optionally filtered.
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string) ([]models.Document, error) {
	tid, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	return s.docs.List(ctx, tid, category, subcategory)
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
