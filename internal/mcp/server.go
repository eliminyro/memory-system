package mcp

import (
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/version"
)

type Server struct {
	memory      *service.MemoryService
	mcp         *mcpsdk.Server
	adminMcp    *mcpsdk.Server
	adminEmails map[string]struct{}
}

func NewServer(memory *service.MemoryService, adminEmails []string) *Server {
	emailSet := make(map[string]struct{}, len(adminEmails))
	for _, e := range adminEmails {
		emailSet[strings.TrimSpace(e)] = struct{}{}
	}

	s := &Server{
		memory:      memory,
		adminEmails: emailSet,
	}

	impl := &mcpsdk.Implementation{
		Name:    "memory-mcp",
		Version: version.Version,
	}

	regularInstructions := `Memory MCP server for persistent context across sessions.

Use search_memory to find relevant memories by semantic similarity and keywords.
Use store_memory to save learnings, preferences, and project state (accepts markdown).
Use get_document to read a specific memory by its hierarchical path.
Use list_documents to browse the memory hierarchy.
Use update_section to modify a single section without rewriting the whole document.
Use delete_document to remove a memory.
Use generate_index to get a compact overview of the knowledge base (summary depth recommended for session start).
Use get_related to find documents semantically similar to a specific document.
Use lint_memory to check knowledge base health — stale docs, sparse content, near-duplicates.

Categories: learnings, preferences, projects
Subcategories vary: learnings has go, infrastructure, cicd, etc.`

	adminInstructions := regularInstructions + `

Admin users can pass tenant_id to any tool to target a specific tenant (including the common pool 00000000-0000-0000-0000-000000000001). Non-admin users cannot use this parameter.

Admin tools: list_tenants, create_tenant, update_tenant, delete_tenant, create_api_key, list_api_keys, revoke_api_key.`

	// Regular server — memory tools only
	s.mcp = mcpsdk.NewServer(impl, &mcpsdk.ServerOptions{Instructions: regularInstructions})
	s.registerTools(s.mcp)

	// Admin server — memory tools + admin tools
	s.adminMcp = mcpsdk.NewServer(impl, &mcpsdk.ServerOptions{Instructions: adminInstructions})
	s.registerTools(s.adminMcp)
	s.registerAdminTools(s.adminMcp)

	return s
}

func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		email := auth.EmailFromContext(r.Context())
		if _, ok := s.adminEmails[email]; ok {
			return s.adminMcp
		}
		return s.mcp
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})
}
