package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// maxContentSize caps a section/document body. The search limit and query-length
// bounds live in the service package (service.MaxSearchLimit / service.MaxQueryLen)
// so the MCP and HTTP read surfaces share one source of truth.
const maxContentSize = 10 << 20 // 10 MB

func (s *Server) registerTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "search_memory",
		Description: "Search memories using semantic similarity and keyword matching. Returns the most relevant sections across all documents. Pass snippet=true for a short match-centered window of each result instead of full content (cheap triage); use get_document to fetch full text. Prompt documents (category prompts) are excluded from unfiltered results — pass category=\"prompts\" or doc_type=\"prompt\" to include them, or read an agent's root prompt with get_document expand=true to assemble its instructions.",
	}, s.SearchMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_document",
		Description: "Get a specific document and all its sections by hierarchical path (category/subcategory/slug). Pass expand=true to also resolve its 'includes' edges and assemble the referenced documents (optionally gated by scope).",
	}, s.GetDocument)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_document_by_id",
		Description: "Get a specific document and all its sections by UUID. Use when you have a doc UUID (e.g. from a cleanup_queue row) and the path-based lookup is ambiguous (multiple docs share a path).",
	}, s.GetDocumentByID)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_document_history",
		Description: "Return a document's mutation history (who changed what, and the prior content), newest first. Visible only to callers who can read the document; parse each entry's `before` field according to its `op_type`.",
	}, s.GetDocumentHistory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "store_memory",
		Description: "Store or update a memory document. Accepts markdown content, splits into sections by ## headings, and generates embeddings for semantic search.",
	}, s.StoreMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_section",
		Description: "Update a single section's content (re-generates its embedding) and/or its heading. Heading-only changes do not re-embed.",
	}, s.UpdateSection)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "put_section",
		Description: "Add or replace ONE section of a document by path + heading (upsert), creating the document if absent and leaving its other sections untouched. Reach for this to add or update a single section without rewriting the whole document — store_memory authors a whole document, and update_section edits by section UUID when you already hold one.",
	}, s.PutSection)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "delete_document",
		Description: "Delete a document and all its sections by path.",
	}, s.DeleteDocument)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "delete_section",
		Description: "Delete a single section by UUID. Deleting a document's last remaining section also deletes the now-empty parent document.",
	}, s.DeleteSection)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_documents",
		Description: "List documents in the memory hierarchy. Filter by category/subcategory and slug_prefix — the cheap path for date-identified docs (e.g. journal by 2026-08). Order by slug/created_at/updated_at/title, asc or desc. Paging (limit/offset) is for large categories.",
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
		Name:        "link_documents",
		Description: "Create a directed typed edge between two documents. edge_type is one of supersedes, derived_from, relates_to, includes. A supersedes edge auto-archives the target (archive_reason=superseded); an includes edge lets get_document expand=true pull the target's content into the source. Requires editor on the source; supersedes also requires editor on the target, others need only viewer. Idempotent: re-creating an identical edge returns the existing one with no side effect.",
	}, s.LinkDocuments)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_document_edges",
		Description: "List a document's typed edges in both directions (outgoing and incoming), including edges to archived endpoints so a supersede trail stays visible. Visible only to callers who can read the document.",
	}, s.ListDocumentEdges)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "resume",
		Description: "Return the latest handoff for a project (where you left off) and, with depth>1, the ordered continues_from chain of prior handoffs. Read-scope gated. Omit project for the latest handoff across all projects; a project with no handoff returns empty.",
	}, s.Resume)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "unlink_documents",
		Description: "Delete a typed edge by its UUID. Requires editor on the edge's source document. Deleting a supersedes edge does NOT un-archive its former target.",
	}, s.UnlinkDocuments)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "lint_memory",
		Description: "Run health checks on the knowledge base. Detects stale docs, sparse content, near-duplicates, and empty categories. All checks are SQL-based, no LLM calls.",
	}, s.LintMemory)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "mark_verified",
		Description: "Mark a section as verified against current source. Use AFTER confirming a stale claim is still accurate (or after updating the content). Resets the freshness clock: clears the needs_verification nudge and unlocks an expired section for all callers. Audited.",
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
		Description: "Read or update feature toggles on your own tenant: staleness_mode (off|advisory|hard), duplicate_guard (bool), cleanup_scan_enabled (bool), metrics_enabled (bool). Any field you omit stays unchanged; omit all for a status read. staleness_mode is a recall-time signal only — off = no signal, advisory = warn in the response, hard = withhold a stale record's content until it is re-verified; none delete or archive. Editing requires MANAGE rights (tenant manager; a personal tenant's owner qualifies), so a plain member of a shared tenant is refused.",
	}, s.UpdateMyTenantSettings)
}

// --- Input types ---

type SearchMemoryInput struct {
	Query       string  `json:"query" jsonschema:"Search query"`
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory: go, infrastructure, hilo, etc."`
	DocType     *string `json:"doc_type,omitempty" jsonschema:"Filter by document type: project_state, audit, learning, preference, tool, reference, journal"`
	Limit       int     `json:"limit,omitempty" jsonschema:"Max results (default 10)"`
	ForceRead   bool    `json:"force_read,omitempty" jsonschema:"Admin-only break-glass: peek an expired section once without resetting its clock (non-admins stay withheld — use mark_verified to unlock). Requires reason. Audited in override_log."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
	Snippet     bool    `json:"snippet,omitempty" jsonschema:"Return a short match-centered snippet of each result's content instead of the full section; use get_document for full text. Default false."`
}

type GetDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	ForceRead   bool    `json:"force_read,omitempty" jsonschema:"Admin-only break-glass: peek an expired section once without resetting its clock (non-admins stay withheld — use mark_verified to unlock). Requires reason. Audited in override_log."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	Expand      bool    `json:"expand,omitempty" jsonschema:"Resolve this document's outgoing 'includes' edges and return the assembled documents plus a resolution manifest. Off by default."`
	Scope       string  `json:"scope,omitempty" jsonschema:"Read-time scope for conditional includes: a whitespace-separated set of tokens. An included document whose scope is non-empty resolves when any token matches any of its patterns via a hierarchical '/'-glob ('**' crosses segments, '*' within one; exact wins). Ignored unless expand is set."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetDocumentByIDInput struct {
	DocumentID string  `json:"document_id" jsonschema:"Document UUID"`
	ForceRead  bool    `json:"force_read,omitempty" jsonschema:"Admin-only break-glass: peek an expired section once without resetting its clock (non-admins stay withheld — use mark_verified to unlock). Requires reason. Audited in override_log."`
	Reason     string  `json:"reason,omitempty" jsonschema:"Required when force_read=true. Brief explanation of why the override is justified."`
	Expand     bool    `json:"expand,omitempty" jsonschema:"Resolve this document's outgoing 'includes' edges and return the assembled documents plus a resolution manifest. Off by default."`
	Scope      string  `json:"scope,omitempty" jsonschema:"Read-time scope for conditional includes: a whitespace-separated set of tokens. An included document whose scope is non-empty resolves when any token matches any of its patterns via a hierarchical '/'-glob ('**' crosses segments, '*' within one; exact wins). Ignored unless expand is set."`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type MarkVerifiedInput struct {
	SectionID string  `json:"section_id" jsonschema:"Section UUID to mark as verified"`
	TenantID  *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetDocumentHistoryInput struct {
	DocumentID string  `json:"document_id" jsonschema:"Document UUID"`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
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
	StalenessMode      *string  `json:"staleness_mode,omitempty" jsonschema:"Enforcement level: off | advisory | hard"`
	DuplicateGuard     *bool    `json:"duplicate_guard,omitempty" jsonschema:"When true, store_memory refuses near-duplicate content"`
	DuplicateThreshold *float64 `json:"duplicate_threshold,omitempty" jsonschema:"Near-duplicate cutoff override, 0<v<=1; omit to inherit the global default"`
	CleanupScanEnabled *bool    `json:"cleanup_scan_enabled,omitempty" jsonschema:"When true, this tenant is included in the nightly near-duplicate scan"`
	MetricsEnabled     *bool    `json:"metrics_enabled,omitempty" jsonschema:"When true, this tenant records access/verify/cleanup usage events for the metrics dashboard"`
}

type StoreMemoryInput struct {
	Category    string  `json:"category" jsonschema:"Document category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory: go, infrastructure, hilo, etc."`
	Slug        string  `json:"slug" jsonschema:"Document slug/filename without extension"`
	Content     string  `json:"content" jsonschema:"Markdown content. Split into sections by ## headings."`
	Force       bool    `json:"force,omitempty" jsonschema:"Bypass duplicate guard. Requires reason. Audited in override_log. Prefer update_section on a returned candidate instead."`
	Reason      string  `json:"reason,omitempty" jsonschema:"Required when force=true. Brief explanation of why this is not a duplicate."`
	Pin         *bool   `json:"pin,omitempty" jsonschema:"Mark the document a pin (never-evict): exempt from access-recency eviction. On re-store, omit to keep the current pin state, or set true/false to change it."`
	Scope       *string `json:"scope,omitempty" jsonschema:"Applicability of any document: empty = always applies, or a whitespace-separated list of '/'-delimited glob patterns ('**' crosses segments, '*' within one) gating conditional includes at read time. Omit to keep the current value; empty string clears it."`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type UpdateSectionInput struct {
	SectionID string  `json:"section_id" jsonschema:"the section UUID to update"`
	Content   *string `json:"content,omitempty" jsonschema:"optional new markdown content for the section; omit to leave content (and its embedding) untouched for a heading-only edit"`
	Heading   *string `json:"heading,omitempty" jsonschema:"optional new heading for the section; empty string clears it"`
	TenantID  *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type DeleteDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	TenantID    *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type DeleteSectionInput struct {
	SectionID string  `json:"section_id" jsonschema:"the section UUID to delete"`
	TenantID  *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type ListDocumentsInput struct {
	Category          *string `json:"category,omitempty" jsonschema:"Filter by category"`
	Subcategory       *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory"`
	SlugPrefix        *string `json:"slug_prefix,omitempty" jsonschema:"Return only documents whose slug begins with this — the cheap path for date-identified docs (journal/YYYY-MM-DD): '2026-08' for a month, '2026' for a year."`
	SubcategoryPrefix *string `json:"subcategory_prefix,omitempty" jsonschema:"Browse a subtree: return docs whose subcategory equals this or begins with it plus '/'. Segment-anchored — 'a11s' matches 'a11s' and 'a11s/platform' but not 'a11something'. Use instead of an exact subcategory to list a branch."`
	OrderBy           *string `json:"order_by,omitempty" jsonschema:"Sort field: slug (default), created_at, updated_at, or title."`
	Order             *string `json:"order,omitempty" jsonschema:"Sort direction: asc (default) or desc."`
	Limit             *int    `json:"limit,omitempty" jsonschema:"Max documents to return; omit for all. Paging is for large categories — prefer slug_prefix to narrow."`
	Offset            *int    `json:"offset,omitempty" jsonschema:"Rows to skip; requires limit."`
	TenantID          *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type PutSectionInput struct {
	Category    string  `json:"category" jsonschema:"Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Optional subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug"`
	Heading     string  `json:"heading" jsonschema:"Section heading (the ## title); matched to upsert by heading"`
	Content     string  `json:"content" jsonschema:"Section content (markdown body under the heading)"`
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

type LinkDocumentsInput struct {
	SourceDocumentID string  `json:"source_document_id" jsonschema:"UUID of the source document (the one making the statement)"`
	TargetDocumentID string  `json:"target_document_id" jsonschema:"UUID of the target document (the one pointed at; archived on supersedes)"`
	EdgeType         string  `json:"edge_type" jsonschema:"One of: supersedes, derived_from, relates_to"`
	TenantID         *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type ListDocumentEdgesInput struct {
	DocumentID string  `json:"document_id" jsonschema:"UUID of the document whose edges to list"`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type ResumeInput struct {
	Project  *string `json:"project,omitempty" jsonschema:"Project (handoff subcategory) to resume. Omit for the latest handoff across all projects."`
	Depth    int     `json:"depth,omitempty" jsonschema:"Max chain length including the latest handoff (default 10). >1 walks the continues_from chain of prior handoffs."`
	TenantID *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type UnlinkDocumentsInput struct {
	EdgeID   string  `json:"edge_id" jsonschema:"UUID of the edge to delete"`
	TenantID *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
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
	if input.Query == "" || len(input.Query) > service.MaxQueryLen {
		return errorResult("query is required and must be <= 10000 characters"), nil, nil
	}
	if input.Limit > service.MaxSearchLimit {
		input.Limit = service.MaxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.Search(ctx, input.Query, input.Category, input.Subcategory, input.DocType, input.Limit, input.ForceRead, input.Reason, tenantOverride, input.Snippet)
	if err != nil {
		return toolErr("search", err)
	}
	return jsonResult(service.NewSearchResponse(results)), nil, nil
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
	var doc *service.DocumentView
	if input.Expand {
		doc, err = s.memory.GetDocumentExpanded(ctx, input.Category, input.Subcategory, input.Slug, input.ForceRead, input.Reason, input.Scope, tenantOverride)
	} else {
		doc, err = s.memory.GetDocument(ctx, input.Category, input.Subcategory, input.Slug, input.ForceRead, input.Reason, tenantOverride)
	}
	if err != nil {
		return toolErr("get document", err)
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
	var doc *service.DocumentView
	if input.Expand {
		doc, err = s.memory.GetDocumentByIDExpanded(ctx, id, input.ForceRead, input.Reason, input.Scope, tenantOverride)
	} else {
		doc, err = s.memory.GetDocumentByID(ctx, id, input.ForceRead, input.Reason, tenantOverride)
	}
	if err != nil {
		return toolErr("get document by id", err)
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) GetDocumentHistory(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentHistoryInput) (*mcpsdk.CallToolResult, any, error) {
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
	entries, err := s.memory.GetDocumentHistory(ctx, id, tenantOverride)
	if err != nil {
		return toolErr("get document history", err)
	}
	return jsonResult(entries), nil, nil
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
		return toolErr("mark verified", err)
	}
	return jsonResult(map[string]string{"status": "verified", "section_id": id.String()}), nil, nil
}

func (s *Server) GetCleanupQueue(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetCleanupQueueInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Limit > service.MaxSearchLimit {
		input.Limit = service.MaxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	rows, err := s.memory.GetCleanupQueue(ctx, input.Limit, input.IncludeResolved, tenantOverride)
	if err != nil {
		return toolErr("get cleanup queue", err)
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
		return toolErr("mark cleanup done", err)
	}
	return jsonResult(map[string]string{"status": "resolved", "queue_id": qid.String()}), nil, nil
}

func (s *Server) UpdateMyTenantSettings(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateMyTenantSettingsInput) (*mcpsdk.CallToolResult, any, error) {
	tenant, err := s.memory.UpdateMyTenantSettings(ctx, input.StalenessMode, input.DuplicateGuard, input.DuplicateThreshold, false, input.CleanupScanEnabled, input.MetricsEnabled)
	if err != nil {
		return toolErr("update my tenant settings", err)
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
		return toolErr("merge documents", err)
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
	result, err := s.memory.StoreDocumentScoped(ctx, input.Category, input.Subcategory, input.Slug, input.Content, input.Force, input.Reason, tenantOverride, input.Pin, input.Scope)
	if err != nil {
		return toolErr("store", err)
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
	if input.SectionID == "" {
		return errorResult("section_id is required"), nil, nil
	}
	// Content nil means "heading-only edit, skip re-embedding" (matches HTTP
	// patchSection); require at least one mutable field so the call isn't a no-op.
	if input.Content == nil && input.Heading == nil {
		return errorResult("at least one of content or heading is required"), nil, nil
	}
	if input.Content != nil && len(*input.Content) > maxContentSize {
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
	section, err := s.memory.UpdateSection(ctx, id, input.Content, input.Heading, tenantOverride)
	if err != nil {
		return toolErr("update section", err)
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
		return toolErr("delete", err)
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) DeleteSection(ctx context.Context, _ *mcpsdk.CallToolRequest, input DeleteSectionInput) (*mcpsdk.CallToolResult, any, error) {
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
	if err := s.memory.DeleteSection(ctx, id, tenantOverride); err != nil {
		return toolErr("delete section", err)
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) ListDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	// Omitted limit stays unbounded (design D2/D4) — only the HTTP browse path paginates.
	opts, err := service.ValidateListOptions(input.SlugPrefix, input.SubcategoryPrefix, input.OrderBy, input.Order, input.Limit, input.Offset)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	docs, err := s.memory.ListDocuments(ctx, input.Category, input.Subcategory, tenantOverride, opts)
	if err != nil {
		return toolErr("list", err)
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

func (s *Server) PutSection(ctx context.Context, _ *mcpsdk.CallToolRequest, input PutSectionInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" || input.Heading == "" || input.Content == "" {
		return errorResult("category, slug, heading, and content are required"), nil, nil
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
	result, err := s.memory.PutSection(ctx, input.Category, input.Subcategory, input.Slug, input.Heading, input.Content, tenantOverride)
	if err != nil {
		return toolErr("put_section", err)
	}
	return jsonResult(map[string]any{
		"status":   "ok",
		"id":       result.Document.ID,
		"path":     result.Path,
		"sections": result.Sections,
	}), nil, nil
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
		return toolErr("generate index", err)
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
	if input.Limit > service.MaxSearchLimit {
		input.Limit = service.MaxSearchLimit
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.GetRelated(ctx, docID, input.Limit, tenantOverride)
	if err != nil {
		return toolErr("get related", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) LinkDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input LinkDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	if input.SourceDocumentID == "" || input.TargetDocumentID == "" {
		return errorResult("source_document_id and target_document_id are required"), nil, nil
	}
	sourceID, err := uuid.Parse(input.SourceDocumentID)
	if err != nil {
		return errorResult("invalid source_document_id: " + err.Error()), nil, nil
	}
	targetID, err := uuid.Parse(input.TargetDocumentID)
	if err != nil {
		return errorResult("invalid target_document_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	result, err := s.memory.CreateEdge(ctx, sourceID, targetID, input.EdgeType, tenantOverride)
	if err != nil {
		return toolErr("link documents", err)
	}
	return jsonResult(result), nil, nil
}

func (s *Server) ListDocumentEdges(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentEdgesInput) (*mcpsdk.CallToolResult, any, error) {
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
	edges, err := s.memory.ListDocumentEdges(ctx, docID, tenantOverride)
	if err != nil {
		return toolErr("list document edges", err)
	}
	return jsonResult(edges), nil, nil
}

func (s *Server) Resume(ctx context.Context, _ *mcpsdk.CallToolRequest, input ResumeInput) (*mcpsdk.CallToolResult, any, error) {
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	result, err := s.memory.Resume(ctx, input.Project, tenantOverride, input.Depth)
	if err != nil {
		return toolErr("resume", err)
	}
	return jsonResult(result), nil, nil
}

func (s *Server) UnlinkDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input UnlinkDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	if input.EdgeID == "" {
		return errorResult("edge_id is required"), nil, nil
	}
	edgeID, err := uuid.Parse(input.EdgeID)
	if err != nil {
		return errorResult("invalid edge_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if err := s.memory.DeleteEdge(ctx, edgeID, tenantOverride); err != nil {
		return toolErr("unlink documents", err)
	}
	return jsonResult(map[string]any{"deleted": true, "edge_id": edgeID}), nil, nil
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
		return toolErr("lint memory", err)
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
