package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerTools() {
	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "search_memory",
		Description: "Search memories using semantic similarity and keyword matching. Returns the most relevant sections across all documents.",
	}, s.SearchMemory)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "get_document",
		Description: "Get a specific document and all its sections by hierarchical path (category/subcategory/slug).",
	}, s.GetDocument)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "store_memory",
		Description: "Store or update a memory document. Accepts markdown content, splits into sections by ## headings, and generates embeddings for semantic search.",
	}, s.StoreMemory)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "update_section",
		Description: "Update a single section's content. Re-generates its embedding automatically.",
	}, s.UpdateSection)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "delete_document",
		Description: "Delete a document and all its sections by path.",
	}, s.DeleteDocument)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "list_documents",
		Description: "List documents in the memory hierarchy. Filter by category and/or subcategory.",
	}, s.ListDocuments)
}

// --- Input types ---

type SearchMemoryInput struct {
	Query       string  `json:"query" jsonschema:"description=Search query"`
	Category    *string `json:"category,omitempty" jsonschema:"description=Filter by category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"description=Filter by subcategory: go, infrastructure, hilo, etc."`
	Limit       int     `json:"limit,omitempty" jsonschema:"description=Max results (default 10)"`
}

type GetDocumentInput struct {
	Category    string  `json:"category" jsonschema:"description=Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"description=Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"description=Document slug"`
}

type StoreMemoryInput struct {
	Category    string  `json:"category" jsonschema:"description=Document category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"description=Document subcategory: go, infrastructure, hilo, etc."`
	Slug        string  `json:"slug" jsonschema:"description=Document slug/filename without extension"`
	Content     string  `json:"content" jsonschema:"description=Markdown content. Split into sections by ## headings."`
}

type UpdateSectionInput struct {
	SectionID string `json:"section_id" jsonschema:"description=Section UUID"`
	Content   string `json:"content" jsonschema:"description=New section content"`
}

type DeleteDocumentInput struct {
	Category    string  `json:"category" jsonschema:"description=Document category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"description=Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"description=Document slug"`
}

type ListDocumentsInput struct {
	Category    *string `json:"category,omitempty" jsonschema:"description=Filter by category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"description=Filter by subcategory"`
}

// --- Handlers ---

func (s *Server) SearchMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input SearchMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Query == "" {
		return errorResult("query is required"), nil, nil
	}
	results, err := s.memory.Search(ctx, input.Query, input.Category, input.Subcategory, input.Limit)
	if err != nil {
		return nil, nil, fmt.Errorf("search: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) GetDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	doc, err := s.memory.GetDocument(ctx, input.Category, input.Subcategory, input.Slug)
	if err != nil {
		return errorResult("document not found: " + err.Error()), nil, nil
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) StoreMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input StoreMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" || input.Content == "" {
		return errorResult("category, slug, and content are required"), nil, nil
	}
	doc, err := s.memory.StoreDocument(ctx, input.Category, input.Subcategory, input.Slug, input.Content)
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
	id, err := uuid.Parse(input.SectionID)
	if err != nil {
		return errorResult("invalid section_id: " + err.Error()), nil, nil
	}
	section, err := s.memory.UpdateSection(ctx, id, input.Content)
	if err != nil {
		return nil, nil, fmt.Errorf("update section: %w", err)
	}
	return jsonResult(section), nil, nil
}

func (s *Server) DeleteDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input DeleteDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	if err := s.memory.DeleteDocument(ctx, input.Category, input.Subcategory, input.Slug); err != nil {
		return errorResult("delete failed: " + err.Error()), nil, nil
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) ListDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	docs, err := s.memory.ListDocuments(ctx, input.Category, input.Subcategory)
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

// --- Helpers ---

func errorResult(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: msg},
		},
	}
}

func jsonResult(v any) *mcpsdk.CallToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult("marshal error: " + err.Error())
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(data)},
		},
	}
}
