package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/middleware"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/version"
)

// Checker is the authorization capability the server needs to pick between the
// regular and admin tool surfaces. *authz.Engine satisfies it.
type Checker interface {
	Check(ctx context.Context, objType, objID, relation, subjType, subjID string) (bool, error)
}

type Server struct {
	memory   *service.MemoryService
	mcp      *mcpsdk.Server
	adminMcp *mcpsdk.Server
	checker  Checker
}

// NewServer builds the regular + admin MCP tool surfaces. checker drives the
// per-request admin split (admin surface iff subject holds system:memory#admin);
// a nil checker fails closed to the regular surface.
func NewServer(memory *service.MemoryService, checker Checker) *Server {
	s := &Server{
		memory:  memory,
		checker: checker,
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

	// Regular server — memory tools + delegated-ACL tools (not admin-only: a
	// tenant#manager who is not a system admin manages grants here; the service
	// enforces the ceiling).
	s.mcp = mcpsdk.NewServer(impl, &mcpsdk.ServerOptions{Instructions: regularInstructions})
	s.registerTools(s.mcp)
	s.registerACLTools(s.mcp)
	s.mcp.AddReceivingMiddleware(logToolCalls)

	// Admin server — memory tools + delegated-ACL tools + admin tools
	s.adminMcp = mcpsdk.NewServer(impl, &mcpsdk.ServerOptions{Instructions: adminInstructions})
	s.registerTools(s.adminMcp)
	s.registerACLTools(s.adminMcp)
	s.registerAdminTools(s.adminMcp)
	s.adminMcp.AddReceivingMiddleware(logToolCalls)

	return s
}

// isAdmin reports whether the context subject is a global admin
// (system:memory#admin). Fails closed on nil checker, no subject, or Check error.
func (s *Server) isAdmin(ctx context.Context) bool {
	if s.checker == nil {
		return false
	}
	subj, ok := auth.SubjectFromContext(ctx)
	if !ok || subj.ID == "" {
		return false
	}
	granted, err := s.checker.Check(ctx, authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin, subj.Type, subj.ID)
	if err != nil {
		slog.Default().Error("mcp admin check errored; treating as non-admin",
			"subject_id", subj.ID, "error", err)
		return false
	}
	return granted
}

func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		if s.isAdmin(r.Context()) {
			return s.adminMcp
		}
		return s.mcp
	}, &mcpsdk.StreamableHTTPOptions{Stateless: true})
}

// logToolCalls is a receiving middleware that logs one metadata line per
// tools/call. Arguments and results are never read — they carry memory content
// and PII; only tool name, tenant, request id, outcome, and duration are logged.
func logToolCalls(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		start := time.Now()
		res, err := next(ctx, method, req)
		logToolOutcome(ctx, req, res, err, time.Since(start))
		return res, err
	}
}

func logToolOutcome(ctx context.Context, req mcpsdk.Request, res mcpsdk.Result, err error, d time.Duration) {
	name := ""
	if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok {
		name = p.Name
	}
	attrs := []any{"tool", name, "duration_ms", d.Milliseconds()}
	if tid := auth.TenantIDFromContext(ctx); tid != uuid.Nil {
		attrs = append(attrs, "tenant_id", tid.String())
	}
	if rid := middleware.RequestIDFromContext(ctx); rid != "" {
		attrs = append(attrs, "request_id", rid)
	}
	switch {
	case err != nil:
		// Protocol/transport error; tool-level internal errors are logged at their source.
		slog.Default().Error("mcp tool call", append(attrs, "outcome", "error", "error", err.Error())...)
	case isErrorResult(res):
		slog.Default().Warn("mcp tool call", append(attrs, "outcome", "error")...)
	default:
		slog.Default().Info("mcp tool call", append(attrs, "outcome", "ok")...)
	}
}

func isErrorResult(res mcpsdk.Result) bool {
	r, ok := res.(*mcpsdk.CallToolResult)
	return ok && r != nil && r.IsError
}
