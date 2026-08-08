package mcp

import (
	"context"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Input types ---

type GrantTenantAccessInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to grant access on"`
	Email    string `json:"email" jsonschema:"User email to grant access to (must already have a tenant_users row)"`
	Relation string `json:"relation" jsonschema:"Relation to grant: viewer, member, or manager"`
}

type RevokeTenantAccessInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to revoke access on"`
	Email    string `json:"email" jsonschema:"User email whose grant to revoke"`
	Relation string `json:"relation" jsonschema:"Relation to revoke: viewer, member, or manager"`
}

type GrantDocumentAccessInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID to grant guest access on"`
	Email      string `json:"email" jsonschema:"User email to grant access to (must already have a tenant_users row)"`
	Relation   string `json:"relation" jsonschema:"Relation to grant: viewer or editor"`
}

type RevokeDocumentAccessInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID to revoke guest access on"`
	Email      string `json:"email" jsonschema:"User email whose grant to revoke"`
	Relation   string `json:"relation" jsonschema:"Relation to revoke: viewer or editor"`
}

type ListTenantGrantsInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to list direct grants for"`
}

type ListDocumentGrantsInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID to list guest grants for"`
}

// --- Registration ---

// registerACLTools mounts the delegated-ACL tools. Called for BOTH the regular
// and admin surfaces (like registerTools): these are not admin-only — a
// delegated tenant#manager who is not a system admin must reach them too. The
// service methods enforce the grant-ceiling and fail closed, so a caller who
// lacks standing is refused there, not here (parity with the /api/acl surface,
// which is deliberately not adminOnly).
func (s *Server) registerACLTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "grant_tenant_access",
		Description: "Grant a user (by email) a viewer/member/manager relation on a tenant. Subject to the grant-ceiling; requires you manage the tenant.",
	}, s.GrantTenantAccess)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "revoke_tenant_access",
		Description: "Revoke a user's viewer/member/manager relation on a tenant. Subject to the same grant-ceiling as granting.",
	}, s.RevokeTenantAccess)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "grant_document_access",
		Description: "Grant a user (by email) per-document guest access (viewer or editor). Requires you manage the document's owning tenant.",
	}, s.GrantDocumentAccess)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "revoke_document_access",
		Description: "Revoke a user's per-document guest access (viewer or editor). Requires you manage the document's owning tenant.",
	}, s.RevokeDocumentAccess)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_tenant_grants",
		Description: "List the direct viewer/member/manager grants on a tenant. Requires you manage the tenant.",
	}, s.ListTenantGrants)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_document_grants",
		Description: "List the per-document guest viewer/editor grants on a document. Requires you manage the document's owning tenant.",
	}, s.ListDocumentGrants)
}

// --- Handlers (thin delegators: parse args -> shared service method -> format) ---

func (s *Server) GrantTenantAccess(ctx context.Context, _ *mcpsdk.CallToolRequest, input GrantTenantAccessInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if err := s.memory.GrantTenantAccess(ctx, tenantID, input.Email, input.Relation); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{
		"status":    "granted",
		"tenant_id": tenantID.String(),
		"email":     input.Email,
		"relation":  input.Relation,
	}), nil, nil
}

func (s *Server) RevokeTenantAccess(ctx context.Context, _ *mcpsdk.CallToolRequest, input RevokeTenantAccessInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if err := s.memory.RevokeTenantAccess(ctx, tenantID, input.Email, input.Relation); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{
		"status":    "revoked",
		"tenant_id": tenantID.String(),
		"email":     input.Email,
		"relation":  input.Relation,
	}), nil, nil
}

func (s *Server) GrantDocumentAccess(ctx context.Context, _ *mcpsdk.CallToolRequest, input GrantDocumentAccessInput) (*mcpsdk.CallToolResult, any, error) {
	docID, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	if err := s.memory.GrantDocumentAccess(ctx, docID, input.Email, input.Relation); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{
		"status":      "granted",
		"document_id": docID.String(),
		"email":       input.Email,
		"relation":    input.Relation,
	}), nil, nil
}

func (s *Server) RevokeDocumentAccess(ctx context.Context, _ *mcpsdk.CallToolRequest, input RevokeDocumentAccessInput) (*mcpsdk.CallToolResult, any, error) {
	docID, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	if err := s.memory.RevokeDocumentAccess(ctx, docID, input.Email, input.Relation); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{
		"status":      "revoked",
		"document_id": docID.String(),
		"email":       input.Email,
		"relation":    input.Relation,
	}), nil, nil
}

func (s *Server) ListTenantGrants(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListTenantGrantsInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	grants, err := s.memory.ListTenantGrants(ctx, tenantID)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(grants), nil, nil
}

func (s *Server) ListDocumentGrants(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentGrantsInput) (*mcpsdk.CallToolResult, any, error) {
	docID, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	grants, err := s.memory.ListDocumentGrants(ctx, docID)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(grants), nil, nil
}
