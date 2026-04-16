package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

type MemoryService struct {
	db          *gorm.DB
	docs        *repository.DocumentRepository
	sections    *repository.SectionRepository
	embedder    EmbeddingProvider
	tenants     *repository.TenantRepository
	keys        *repository.APIKeyRepository
	lint        *repository.LintRepository
	thresholds  *staleness.ThresholdStore
	overrides   *repository.OverrideLogRepository
	adminEmails map[string]struct{}
}

// NewMemoryService constructs the service. Optional deps (tenants, keys, lint,
// thresholds, overrides) may be nil when the service is used outside the MCP
// server (e.g. the import CLI) — nil deps disable the features that need them.
func NewMemoryService(
	db *gorm.DB,
	docs *repository.DocumentRepository,
	sections *repository.SectionRepository,
	embedder EmbeddingProvider,
	tenants *repository.TenantRepository,
	keys *repository.APIKeyRepository,
	lint *repository.LintRepository,
	thresholds *staleness.ThresholdStore,
	overrides *repository.OverrideLogRepository,
	adminEmails []string,
) *MemoryService {
	ae := make(map[string]struct{}, len(adminEmails))
	for _, e := range adminEmails {
		ae[strings.TrimSpace(e)] = struct{}{}
	}
	return &MemoryService{
		db:          db,
		docs:        docs,
		sections:    sections,
		embedder:    embedder,
		tenants:     tenants,
		keys:        keys,
		lint:        lint,
		thresholds:  thresholds,
		overrides:   overrides,
		adminEmails: ae,
	}
}

// resolveTenant resolves the effective tenant ID.
func (s *MemoryService) resolveTenant(ctx context.Context, overrideID *uuid.UUID) (uuid.UUID, error) {
	tid := auth.TenantIDFromContext(ctx)
	if tid == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: missing tenant ID in context", apperr.ErrInvalidInput)
	}
	if overrideID == nil {
		return tid, nil
	}
	email := auth.EmailFromContext(ctx)
	if _, ok := s.adminEmails[email]; !ok {
		return uuid.Nil, fmt.Errorf("%w: tenant_id override requires admin privileges", apperr.ErrInvalidInput)
	}
	if _, err := s.tenants.GetByID(ctx, *overrideID); err != nil {
		return uuid.Nil, fmt.Errorf("%w: target tenant %s not found", apperr.ErrInvalidInput, overrideID)
	}
	return *overrideID, nil
}

func (s *MemoryService) isAdmin(ctx context.Context) bool {
	email := auth.EmailFromContext(ctx)
	_, ok := s.adminEmails[email]
	return ok
}

// Search performs hybrid semantic + keyword search, applying staleness filter.
// When forceRead is true, Reason is required and the override is audited.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int, forceRead bool, reason string, overrideID *uuid.UUID) ([]repository.SearchResult, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	results, err := s.sections.HybridSearch(ctx, repository.SearchParams{
		TenantID:    tid,
		Embedding:   embedding,
		Query:       query,
		Category:    category,
		Subcategory: subcategory,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	if s.thresholds != nil {
		results, err = applyStalenessToSearchResults(ctx, s.thresholds, results, forceRead)
		if err != nil {
			return nil, err
		}
	}
	if forceRead && s.overrides != nil {
		_ = s.overrides.Log(ctx, repository.OverrideEvent{
			TenantID:     tid,
			Tool:         models.OverrideToolSearchMemory,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	return results, nil
}

// GetDocument fetches a document with all sections by path, applying staleness
// filter to each section. forceRead + reason override the filter and log to
// override_log.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string, forceRead bool, reason string, overrideID *uuid.UUID) (*DocumentView, error) {
	if forceRead && strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when force_read=true", apperr.ErrInvalidInput)
	}
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	doc, err := s.docs.GetByPath(ctx, tid, category, subcategory, slug)
	if err != nil {
		return nil, err
	}
	view, err := buildDocumentView(ctx, s.thresholds, doc, forceRead)
	if err != nil {
		return nil, err
	}
	if forceRead && s.overrides != nil {
		docID := doc.ID
		_ = s.overrides.Log(ctx, repository.OverrideEvent{
			TenantID:     tid,
			Tool:         models.OverrideToolGetDocument,
			TargetID:     &docID,
			OverrideType: models.OverrideTypeForceRead,
			Reason:       reason,
		})
	}
	return &view, nil
}

// MarkVerified stamps verified_at = NOW() on a section. Agents call this after
// confirming a claim against current source.
func (s *MemoryService) MarkVerified(ctx context.Context, sectionID uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}
	return s.sections.MarkVerified(ctx, tid, sectionID)
}

// StoreDocument parses markdown content into sections, embeds them, and stores.
// Embeddings are generated before any DB mutations to avoid partial state on failure.
func (s *MemoryService) StoreDocument(ctx context.Context, category string, subcategory *string, slug, content string, overrideID *uuid.UUID) (*models.Document, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
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
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content string, overrideID *uuid.UUID) (*models.Section, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	section, err := s.sections.GetByID(ctx, tid, sectionID)
	if err != nil {
		return nil, fmt.Errorf("get section: %w", err)
	}

	if section.Document != nil && section.Document.TenantID != tid && !s.isAdmin(ctx) {
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
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		doc, err := txDocs.GetByPath(ctx, tid, category, subcategory, slug)
		if err != nil {
			return err
		}
		if doc.TenantID != tid && !s.isAdmin(ctx) {
			return fmt.Errorf("%w: cannot delete common pool document", apperr.ErrInvalidInput)
		}

		if err := txSections.DeleteByDocumentID(ctx, doc.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}
		if err := txDocs.Delete(ctx, doc.TenantID, doc.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}

		return nil
	})
}

// ListDocuments lists documents, optionally filtered.
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string, overrideID *uuid.UUID) ([]models.Document, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.docs.List(ctx, tid, category, subcategory)
}

// --- Admin operations ---

func (s *MemoryService) requireAdmin(ctx context.Context) error {
	if !s.isAdmin(ctx) {
		return fmt.Errorf("%w: admin privileges required", apperr.ErrInvalidInput)
	}
	return nil
}

func (s *MemoryService) ListTenants(ctx context.Context) ([]models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.tenants.List(ctx)
}

func (s *MemoryService) CreateTenant(ctx context.Context, name, email string) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	tenant := &models.Tenant{Name: name, Email: email}
	if err := s.tenants.Create(ctx, tenant); err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return tenant, nil
}

func (s *MemoryService) UpdateTenant(ctx context.Context, id uuid.UUID, name, email *string) (*models.Tenant, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	tenant, err := s.tenants.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		tenant.Name = *name
	}
	if email != nil {
		tenant.Email = *email
	}
	if err := s.tenants.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("update tenant: %w", err)
	}
	return tenant, nil
}

func (s *MemoryService) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if id == models.BootstrapTenantID {
		return fmt.Errorf("%w: cannot delete the bootstrap tenant", apperr.ErrInvalidInput)
	}
	return s.tenants.Delete(ctx, id)
}

func (s *MemoryService) CreateAPIKey(ctx context.Context, tenantID uuid.UUID, label string) (string, *models.APIKey, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return "", nil, err
	}
	if _, err := s.tenants.GetByID(ctx, tenantID); err != nil {
		return "", nil, err
	}
	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		return "", nil, fmt.Errorf("generate key: %w", err)
	}
	key := &models.APIKey{
		TenantID: tenantID,
		KeyHash:  hash,
		Label:    label,
		Prefix:   auth.KeyPrefix(plaintext),
	}
	if err := s.keys.Create(ctx, key); err != nil {
		return "", nil, fmt.Errorf("create key: %w", err)
	}
	return plaintext, key, nil
}

func (s *MemoryService) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]models.APIKey, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.keys.ListByTenant(ctx, tenantID)
}

func (s *MemoryService) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	return s.keys.Revoke(ctx, id)
}

func (s *MemoryService) GenerateIndex(ctx context.Context, depth string, category *string, overrideID *uuid.UUID) ([]repository.IndexEntry, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.docs.GenerateIndex(ctx, tid, repository.IndexDepth(depth), category)
}

func (s *MemoryService) GetRelated(ctx context.Context, documentID uuid.UUID, limit int, overrideID *uuid.UUID) ([]repository.RelatedResult, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.sections.GetRelated(ctx, tid, documentID, limit)
}

func (s *MemoryService) LintMemory(ctx context.Context, checks []string, thresholds *repository.LintThresholds, overrideID *uuid.UUID) ([]repository.LintFinding, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	t := repository.DefaultLintThresholds()
	if thresholds != nil {
		t = *thresholds
	}

	type lintCheck struct {
		name string
		fn   func(context.Context, uuid.UUID, repository.LintThresholds) ([]repository.LintFinding, error)
	}
	allChecks := []lintCheck{
		{"stale", s.lint.CheckStale},
		{"sparse", s.lint.CheckSparse},
		{"near_duplicate", s.lint.CheckNearDuplicates},
		{"empty_category", s.lint.CheckEmptyCategories},
	}

	// Filter to requested checks if specified
	requested := make(map[string]struct{}, len(checks))
	for _, c := range checks {
		requested[c] = struct{}{}
	}

	var findings []repository.LintFinding
	for _, check := range allChecks {
		if len(checks) > 0 {
			if _, ok := requested[check.name]; !ok {
				continue
			}
		}
		results, err := check.fn(ctx, tid, t)
		if err != nil {
			return nil, err
		}
		findings = append(findings, results...)
	}
	return findings, nil
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
