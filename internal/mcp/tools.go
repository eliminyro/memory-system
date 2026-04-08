package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/eliminyro/memory-mcp/internal/errors"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

const (
	maxContentSize = 10 << 20 // 10 MB
	maxQueryLen    = 10_000
	maxSearchLimit = 100
	maxFieldLen    = 100
)

var validSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (s *Server) registerTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "search_memory",
		Description: "Search memories using semantic similarity and keyword matching. Returns the most relevant sections across all documents.",
	}, s.SearchMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_document",
		Description: "Get a specific document and all its sections by hierarchical path (category/subcategory/slug).",
	}, s.GetDocument)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "store_memory",
		Description: "Store or update a memory document. Accepts markdown content, splits into sections by ## headings, and generates embeddings for semantic search.",
	}, s.StoreMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_section",
		Description: "Update a single section's content. Re-generates its embedding automatically.",
	}, s.UpdateSection)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "delete_document",
		Description: "Delete a document and all its sections by path.",
	}, s.DeleteDocument)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_documents",
		Description: "List documents in the memory hierarchy. Filter by category and/or subcategory.",
	}, s.ListDocuments)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "generate_index",
		Description: "Generate a tiered catalog of the knowledge base. Use depth='summary' for a compact overview (one line per subcategory), 'category' for doc-level detail, or 'full' for everything.",
	}, s.GenerateIndex)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_related",
		Description: "Find documents semantically related to a given document. Uses cosine similarity between section embeddings.",
	}, s.GetRelated)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "lint_memory",
		Description: "Run health checks on the knowledge base. Detects stale docs, sparse content, near-duplicates, and empty categories. All checks are SQL-based, no LLM calls.",
	}, s.LintMemory)
}

// --- Input types ---

type SearchMemoryInput struct {
	Query       string  `json:"query" jsonschema:"Search query"`
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory: go, infrastructure, hilo, etc."`
	Limit       int     `json:"limit,omitempty" jsonschema:"Max results (default 10)"`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type StoreMemoryInput struct {
	Category    string  `json:"category" jsonschema:"Document category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory: go, infrastructure, hilo, etc."`
	Slug        string  `json:"slug" jsonschema:"Document slug/filename without extension"`
	Content     string  `json:"content" jsonschema:"Markdown content. Split into sections by ## headings."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type UpdateSectionInput struct {
	SectionID string  `json:"section_id" jsonschema:"Section UUID"`
	Content   string  `json:"content" jsonschema:"New section content"`
	TenantID  *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type DeleteDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type ListDocumentsInput struct {
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory"`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GenerateIndexInput struct {
	Depth    string  `json:"depth,omitempty" jsonschema:"Index depth: summary (default), category, or full"`
	Category *string `json:"category,omitempty" jsonschema:"Filter to specific category"`
	TenantID *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetRelatedInput struct {
	DocumentID string  `json:"document_id" jsonschema:"UUID of the target document"`
	Limit      int     `json:"limit,omitempty" jsonschema:"Max results (default 5)"`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type LintMemoryInput struct {
	Checks     []string                   `json:"checks,omitempty" jsonschema:"Filter to specific checks: stale, sparse, near_duplicate, empty_category"`
	Thresholds *repository.LintThresholds `json:"thresholds,omitempty" jsonschema:"Override default thresholds"`
	TenantID   *string                    `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

// --- Helpers ---

func parseTenantOverride(raw *string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant_id: %w", err)
	}
	return &id, nil
}

// --- Handlers ---

func (s *Server) SearchMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input SearchMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Query == "" || len(input.Query) > maxQueryLen {
		return errorResult("query is required and must be <= 10000 characters"), nil, nil
	}
	if input.Limit > maxSearchLimit {
		input.Limit = maxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.Search(ctx, input.Query, input.Category, input.Subcategory, input.Limit, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("search: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) GetDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	if err := validatePath(input.Category, input.Slug); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	doc, err := s.memory.GetDocument(ctx, input.Category, input.Subcategory, input.Slug, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("get document: %w", err)
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) StoreMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input StoreMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" || input.Content == "" {
		return errorResult("category, slug, and content are required"), nil, nil
	}
	if len(input.Content) > maxContentSize {
		return errorResult("content exceeds 10MB limit"), nil, nil
	}
	if err := validatePath(input.Category, input.Slug); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	doc, err := s.memory.StoreDocument(ctx, input.Category, input.Subcategory, input.Slug, input.Content, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	return jsonResult(map[string]any{
		"id":       doc.ID,
		"path":     doc.Path(),
		"sections": len(doc.Sections),
	}), nil, nil
}

func (s *Server) UpdateSection(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateSectionInput) (*mcpsdk.CallToolResult, any, error) {
	if input.SectionID == "" || input.Content == "" {
		return errorResult("section_id and content are required"), nil, nil
	}
	if len(input.Content) > maxContentSize {
		return errorResult("content exceeds 10MB limit"), nil, nil
	}
	id, err := uuid.Parse(input.SectionID)
	if err != nil {
		return errorResult("invalid section_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	section, err := s.memory.UpdateSection(ctx, id, input.Content, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("update section: %w", err)
	}
	return jsonResult(section), nil, nil
}

func (s *Server) DeleteDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input DeleteDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	if err := validatePath(input.Category, input.Slug); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if err := s.memory.DeleteDocument(ctx, input.Category, input.Subcategory, input.Slug, tenantOverride); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("delete: %w", err)
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) ListDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	docs, err := s.memory.ListDocuments(ctx, input.Category, input.Subcategory, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("list: %w", err)
	}
	// Return compact listing
	type docEntry struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	entries := make([]docEntry, len(docs))
	for i, d := range docs {
		entries[i] = docEntry{Path: d.Path(), Title: d.Title}
	}
	return jsonResult(entries), nil, nil
}

func (s *Server) GenerateIndex(ctx context.Context, _ *mcpsdk.CallToolRequest, input GenerateIndexInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Depth == "" {
		input.Depth = "summary"
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	entries, err := s.memory.GenerateIndex(ctx, input.Depth, input.Category, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("generate index: %w", err)
	}
	return jsonResult(entries), nil, nil
}

func (s *Server) GetRelated(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetRelatedInput) (*mcpsdk.CallToolResult, any, error) {
	if input.DocumentID == "" {
		return errorResult("document_id is required"), nil, nil
	}
	docID, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.GetRelated(ctx, docID, input.Limit, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("get related: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) LintMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input LintMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	findings, err := s.memory.LintMemory(ctx, input.Checks, input.Thresholds, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("lint memory: %w", err)
	}
	return jsonResult(findings), nil, nil
}

// --- Helpers ---

func validatePath(category, slug string) error {
	if len(category) > maxFieldLen || len(slug) > maxFieldLen {
		return fmt.Errorf("category and slug must be <= %d characters", maxFieldLen)
	}
	if !validSlug.MatchString(category) {
		return fmt.Errorf("invalid category format: must be alphanumeric with .-_")
	}
	if !validSlug.MatchString(slug) {
		return fmt.Errorf("invalid slug format: must be alphanumeric with .-_")
	}
	return nil
}

func errorResult(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: msg},
		},
	}
}

func jsonResult(v any) *mcpsdk.CallToolResult {
	data, err := json.Marshal(v)
	if err != nil {
		return errorResult("marshal error: " + err.Error())
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(data)},
		},
	}
}
