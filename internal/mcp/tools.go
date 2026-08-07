package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

const (
	maxContentSize = 10 << 20 // 10 MB
	maxQueryLen    = 10_000
	maxSearchLimit = 100
)

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
		Name:        "get_document_by_id",
		Description: "Get a specific document and all its sections by UUID. Use when you have a doc UUID (e.g. from a cleanup_queue row) and the path-based lookup is ambiguous (multiple docs share a path).",
	}, s.GetDocumentByID)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "store_memory",
		Description: "Store or update a memory document. Accepts markdown content, splits into sections by ## headings, and generates embeddings for semantic search.",
	}, s.StoreMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_section",
		Description: "Update a single section's content (re-generates its embedding) and/or its heading. Heading-only changes do not re-embed.",
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

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "mark_verified",
		Description: "Mark a section as verified against current source. Use AFTER confirming a stale claim is still accurate (or after updating the content). Clears the needs_verification guard for the staleness threshold window.",
	}, s.MarkVerified)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_cleanup_queue",
		Description: "Return pending near-duplicate candidates detected by the nightly cleanup scan. Each entry names two documents that collide above threshold; the cleanup agent reads these, merges with merge_documents, and resolves with mark_cleanup_done.",
	}, s.GetCleanupQueue)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "mark_cleanup_done",
		Description: "Resolve a cleanup queue entry. resolution must be 'merged', 'ignored', or 'false_positive'. When merged, pass merged_into with the surviving document's ID.",
	}, s.MarkCleanupDone)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "merge_documents",
		Description: "Merge two documents: move the caller-specified sections (from either doc) into the winner, delete the rest, and delete the loser. The cleanup agent decides which sections to keep; this tool is the dumb mechanical merge.",
	}, s.MergeDocuments)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_my_tenant_settings",
		Description: "Update feature toggles on your own tenant: staleness_mode (off|advisory|hard), duplicate_guard (bool), cleanup_scan_enabled (bool). Any field you omit stays unchanged. Defaults are the safest — opt in to enforcement explicitly.",
	}, s.UpdateMyTenantSettings)
}

// --- Input types ---

type SearchMemoryInput struct {
	Query       string  `json:"query" jsonschema:"Search query"`
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory: go, infrastructure, hilo, etc."`
	Limit       int     `json:"limit,omitempty" jsonschema:"Max results (default 10)"`
	ForceRead   bool    `json:"force_read,omitempty" jsonschema:"Bypass staleness guard. Requires reason. Audited in override_log."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	ForceRead   bool    `json:"force_read,omitempty" jsonschema:"Bypass staleness guard. Requires reason. Audited in override_log."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetDocumentByIDInput struct {
	DocumentID string  `json:"document_id" jsonschema:"Document UUID"`
	ForceRead  bool    `json:"force_read,omitempty" jsonschema:"Bypass staleness guard. Requires reason. Audited in override_log."`
	Reason     string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type MarkVerifiedInput struct {
	SectionID string  `json:"section_id" jsonschema:"Section UUID to mark as verified"`
	TenantID  *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetCleanupQueueInput struct {
	Limit           int     `json:"limit,omitempty" jsonschema:"Max entries to return (default 50)"`
	IncludeResolved bool    `json:"include_resolved,omitempty" jsonschema:"Include already-resolved rows in the result (default false — only pending)"`
	TenantID        *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type MarkCleanupDoneInput struct {
	QueueID    string  `json:"queue_id" jsonschema:"Cleanup queue entry UUID"`
	Resolution string  `json:"resolution" jsonschema:"One of: merged, ignored, false_positive"`
	Note       string  `json:"note,omitempty" jsonschema:"Optional note recording why/how this was resolved"`
	MergedInto *string `json:"merged_into,omitempty" jsonschema:"Document UUID that won the merge. Required when resolution=merged."`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type MergeDocumentsInput struct {
	WinnerID       string   `json:"winner_id" jsonschema:"Document UUID that survives the merge"`
	LoserID        string   `json:"loser_id" jsonschema:"Document UUID that gets deleted after sections are moved"`
	SectionsToKeep []string `json:"sections_to_keep" jsonschema:"Ordered list of section UUIDs (from either doc) to retain. Order determines final ordinal."`
	TenantID       *string  `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type UpdateMyTenantSettingsInput struct {
	StalenessMode      *string `json:"staleness_mode,omitempty" jsonschema:"Enforcement level: off | advisory | hard"`
	DuplicateGuard     *bool   `json:"duplicate_guard,omitempty" jsonschema:"When true, store_memory refuses near-duplicate content"`
	CleanupScanEnabled *bool   `json:"cleanup_scan_enabled,omitempty" jsonschema:"When true, this tenant is included in the nightly near-duplicate scan"`
}

type StoreMemoryInput struct {
	Category    string  `json:"category" jsonschema:"Document category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory: go, infrastructure, hilo, etc."`
	Slug        string  `json:"slug" jsonschema:"Document slug/filename without extension"`
	Content     string  `json:"content" jsonschema:"Markdown content. Split into sections by ## headings."`
	Force       bool    `json:"force,omitempty" jsonschema:"Bypass duplicate guard. Requires reason. Audited in override_log. Prefer update_section on a returned candidate instead."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force=true. Brief explanation of why this is not a duplicate."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type UpdateSectionInput struct {
	SectionID string  `json:"section_id" jsonschema:"the section UUID to update"`
	Content   string  `json:"content" jsonschema:"the new markdown content for the section"`
	Heading   *string `json:"heading,omitempty" jsonschema:"optional new heading for the section; empty string clears it"`
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
	results, err := s.memory.Search(ctx, input.Query, input.Category, input.Subcategory, input.Limit, input.ForceRead, input.Reason, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("search: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) GetDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	if err := validatePath(input.Category, input.Slug, input.Subcategory); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	doc, err := s.memory.GetDocument(ctx, input.Category, input.Subcategory, input.Slug, input.ForceRead, input.Reason, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("get document: %w", err)
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) GetDocumentByID(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentByIDInput) (*mcpsdk.CallToolResult, any, error) {
	if input.DocumentID == "" {
		return errorResult("document_id is required"), nil, nil
	}
	id, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	doc, err := s.memory.GetDocumentByID(ctx, id, input.ForceRead, input.Reason, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("get document by id: %w", err)
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) MarkVerified(ctx context.Context, _ *mcpsdk.CallToolRequest, input MarkVerifiedInput) (*mcpsdk.CallToolResult, any, error) {
	if input.SectionID == "" {
		return errorResult("section_id is required"), nil, nil
	}
	id, err := uuid.Parse(input.SectionID)
	if err != nil {
		return errorResult("invalid section_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if err := s.memory.MarkVerified(ctx, id, tenantOverride); err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("mark verified: %w", err)
	}
	return jsonResult(map[string]string{"status": "verified", "section_id": id.String()}), nil, nil
}

func (s *Server) GetCleanupQueue(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetCleanupQueueInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Limit > maxSearchLimit {
		input.Limit = maxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	rows, err := s.memory.GetCleanupQueue(ctx, input.Limit, input.IncludeResolved, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("get cleanup queue: %w", err)
	}
	return jsonResult(rows), nil, nil
}

func (s *Server) MarkCleanupDone(ctx context.Context, _ *mcpsdk.CallToolRequest, input MarkCleanupDoneInput) (*mcpsdk.CallToolResult, any, error) {
	if input.QueueID == "" || input.Resolution == "" {
		return errorResult("queue_id and resolution are required"), nil, nil
	}
	qid, err := uuid.Parse(input.QueueID)
	if err != nil {
		return errorResult("invalid queue_id: " + err.Error()), nil, nil
	}
	var mergedInto *uuid.UUID
	if input.MergedInto != nil && *input.MergedInto != "" {
		id, err := uuid.Parse(*input.MergedInto)
		if err != nil {
			return errorResult("invalid merged_into: " + err.Error()), nil, nil
		}
		mergedInto = &id
	}
	if input.Resolution == "merged" && mergedInto == nil {
		return errorResult("merged_into is required when resolution=merged"), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if err := s.memory.MarkCleanupDone(ctx, qid, input.Resolution, input.Note, mergedInto, tenantOverride); err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("mark cleanup done: %w", err)
	}
	return jsonResult(map[string]string{"status": "resolved", "queue_id": qid.String()}), nil, nil
}

func (s *Server) UpdateMyTenantSettings(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateMyTenantSettingsInput) (*mcpsdk.CallToolResult, any, error) {
	tenant, err := s.memory.UpdateMyTenantSettings(ctx, input.StalenessMode, input.DuplicateGuard, input.CleanupScanEnabled)
	if err != nil {
		if errors.Is(err, apperr.ErrInvalidInput) || errors.Is(err, apperr.ErrNotFound) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("update my tenant settings: %w", err)
	}
	return jsonResult(tenant), nil, nil
}

func (s *Server) MergeDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input MergeDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	if input.WinnerID == "" || input.LoserID == "" || len(input.SectionsToKeep) == 0 {
		return errorResult("winner_id, loser_id, and sections_to_keep are required"), nil, nil
	}
	winnerID, err := uuid.Parse(input.WinnerID)
	if err != nil {
		return errorResult("invalid winner_id: " + err.Error()), nil, nil
	}
	loserID, err := uuid.Parse(input.LoserID)
	if err != nil {
		return errorResult("invalid loser_id: " + err.Error()), nil, nil
	}
	keep := make([]uuid.UUID, 0, len(input.SectionsToKeep))
	for i, raw := range input.SectionsToKeep {
		id, err := uuid.Parse(raw)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid sections_to_keep[%d]: %s", i, err.Error())), nil, nil
		}
		keep = append(keep, id)
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	result, err := s.memory.MergeDocuments(ctx, winnerID, loserID, keep, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("merge documents: %w", err)
	}
	return jsonResult(result), nil, nil
}

func (s *Server) StoreMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input StoreMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" || input.Content == "" {
		return errorResult("category, slug, and content are required"), nil, nil
	}
	if len(input.Content) > maxContentSize {
		return errorResult("content exceeds 10MB limit"), nil, nil
	}
	if err := validatePath(input.Category, input.Slug, input.Subcategory); err != nil {
		return errorResult(err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	result, err := s.memory.StoreDocument(ctx, input.Category, input.Subcategory, input.Slug, input.Content, input.Force, input.Reason, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	if result.Status == "similar_exists" {
		return jsonResult(map[string]any{
			"status":     "similar_exists",
			"candidates": result.Candidates,
			"hint":       "Prefer update_section on the closest candidate, or resubmit with force=true and a reason if the collision is a false positive.",
		}), nil, nil
	}
	return jsonResult(map[string]any{
		"status":   "ok",
		"id":       result.Document.ID,
		"path":     result.Path,
		"sections": result.Sections,
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
	section, err := s.memory.UpdateSection(ctx, id, &input.Content, input.Heading, tenantOverride)
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
	if err := validatePath(input.Category, input.Slug, input.Subcategory); err != nil {
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
	// Compact listing; ID lets clients map a doc UUID (e.g. cleanup_queue row) to a path.
	type docEntry struct {
		ID    string `json:"id"`
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	entries := make([]docEntry, len(docs))
	for i, d := range docs {
		entries[i] = docEntry{ID: d.ID.String(), Path: d.Path(), Title: d.Title}
	}
	return jsonResult(entries), nil, nil
}

func (s *Server) GenerateIndex(ctx context.Context, _ *mcpsdk.CallToolRequest, input GenerateIndexInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Depth == "" {
		input.Depth = "summary"
	}
	switch input.Depth {
	case "summary", "category", "full":
		// valid
	default:
		return errorResult(fmt.Sprintf("invalid depth %q: must be summary, category, or full", input.Depth)), nil, nil
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
	if input.Limit > maxSearchLimit {
		input.Limit = maxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.GetRelated(ctx, docID, input.Limit, tenantOverride)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) || errors.Is(err, apperr.ErrInvalidInput) {
			return errorResult(err.Error()), nil, nil
		}
		return nil, nil, fmt.Errorf("get related: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) LintMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input LintMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	validChecks := map[string]bool{"stale": true, "sparse": true, "near_duplicate": true, "empty_category": true}
	for _, c := range input.Checks {
		if !validChecks[c] {
			return errorResult(fmt.Sprintf("invalid check %q: must be one of stale, sparse, near_duplicate, empty_category", c)), nil, nil
		}
	}
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

// validatePath delegates to the shared models contract so the MCP and HTTP
// write surfaces enforce identical path rules and per-field length caps (the
// category cap now matches its varchar(50) column instead of the old 100).
func validatePath(category, slug string, subcategory *string) error {
	return models.ValidateDocumentPath(category, slug, subcategory)
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
