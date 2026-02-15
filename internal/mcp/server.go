package mcp

import (
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eliminyro/memory-mcp/internal/service"
)

type Server struct {
	memory *service.MemoryService
	mcp    *mcpsdk.Server
}

func NewServer(memory *service.MemoryService) *Server {
	s := &Server{
		memory: memory,
	}

	impl := &mcpsdk.Implementation{
		Name:    "memory-mcp",
		Version: "1.0.0",
	}

	opts := &mcpsdk.ServerOptions{
		Instructions: `Memory MCP server for persistent context across sessions.

Use search_memory to find relevant memories by semantic similarity and keywords.
Use store_memory to save learnings, preferences, and project state (accepts markdown).
Use get_document to read a specific memory by its hierarchical path.
Use list_documents to browse the memory hierarchy.
Use update_section to modify a single section without rewriting the whole document.
Use delete_document to remove a memory.

Categories: learnings, preferences, projects
Subcategories vary: learnings has go, infrastructure, cicd, etc.`,
	}

	s.mcp = mcpsdk.NewServer(impl, opts)
	s.registerTools()

	return s
}

func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.mcp
	}, nil)
}
