package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/service"
)

const maxAdminFieldLen = 200

// --- Input types ---

type ListTenantsInput struct{}

type CreateTenantInput struct {
	Name  string `json:"name" jsonschema:"Tenant name (required, max 200 chars)"`
	Email string `json:"email,omitempty" jsonschema:"Tenant email (optional)"`
}

type UpdateTenantInput struct {
	TenantID           string  `json:"tenant_id" jsonschema:"Tenant UUID to update"`
	Name               *string `json:"name,omitempty" jsonschema:"New tenant name (max 200 chars)"`
	Email              *string `json:"email,omitempty" jsonschema:"New tenant email"`
	StalenessMode      *string `json:"staleness_mode,omitempty" jsonschema:"Staleness enforcement: off, advisory, or hard"`
	DuplicateGuard     *bool   `json:"duplicate_guard,omitempty" jsonschema:"Refuse store_memory on near-duplicate content (default false)"`
	CleanupScanEnabled *bool   `json:"cleanup_scan_enabled,omitempty" jsonschema:"Include this tenant in the nightly near-duplicate scan (default false)"`
}

type DeleteTenantInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to delete"`
}

type CreateAPIKeyInput struct {
	TenantID      string  `json:"tenant_id" jsonschema:"Tenant UUID to create key for"`
	Label         string  `json:"label" jsonschema:"Key label (required, max 200 chars)"`
	SubjectID     *string `json:"subject_id,omitempty" jsonschema:"Optional unified authorization subject id to pin the key to; omit for the tenant service principal"`
	ExpiresInDays *int    `json:"expires_in_days,omitempty" jsonschema:"Optional TTL in days; omit for a key that never expires"`
}

type ListAPIKeysInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to list keys for"`
}

type RevokeAPIKeyInput struct {
	KeyID string `json:"key_id" jsonschema:"API key UUID to revoke"`
}

type RotateAPIKeyInput struct {
	KeyID      string `json:"key_id" jsonschema:"API key UUID to rotate; a replacement is issued and this one retired"`
	GraceHours *int   `json:"grace_hours,omitempty" jsonschema:"Optional grace window in hours during which the old key stays valid; omit to revoke it immediately"`
}

type GrantUserInput struct {
	Email    string `json:"email" jsonschema:"User email to grant tenant access to"`
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to grant access on"`
	Role     string `json:"role,omitempty" jsonschema:"Role: member (default) or admin"`
}

type ListUsersInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to list user mappings for"`
}

type UpdateUserRoleInput struct {
	Email string `json:"email" jsonschema:"User email whose role to change"`
	Role  string `json:"role" jsonschema:"New role: member or admin"`
}

type RevokeUserInput struct {
	Email string `json:"email" jsonschema:"User email whose tenant access to revoke"`
}

// --- Registration ---

func (s *Server) registerAdminTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_tenants",
		Description: "List all tenants. Admin only.",
	}, s.ListTenants)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "create_tenant",
		Description: "Create a new tenant. Admin only.",
	}, s.CreateTenant)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_tenant",
		Description: "Update an existing tenant's name and/or email. Admin only.",
	}, s.UpdateTenant)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "delete_tenant",
		Description: "Delete a tenant. Cannot delete the bootstrap tenant. Admin only.",
	}, s.DeleteTenant)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "create_api_key",
		Description: "Create an API key for a tenant. Returns the plaintext key (shown only once). Admin only.",
	}, s.CreateAPIKey)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_api_keys",
		Description: "List API keys for a tenant. Admin only.",
	}, s.ListAPIKeys)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "revoke_api_key",
		Description: "Revoke an API key. Admin only.",
	}, s.RevokeAPIKey)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "rotate_api_key",
		Description: "Rotate an API key: issue a replacement and retire the predecessor (immediately, or after an optional grace window). Returns the new plaintext key once. Admin only.",
	}, s.RotateAPIKey)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "grant_user",
		Description: "Grant a human email access to a tenant with a role (member/admin). Admin only.",
	}, s.GrantUser)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_users",
		Description: "List the email->tenant user mappings for a tenant. Admin only.",
	}, s.ListUsers)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_user_role",
		Description: "Change a user's role (member/admin). Admin only.",
	}, s.UpdateUserRole)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "revoke_user",
		Description: "Revoke a user's tenant access by email. Admin only.",
	}, s.RevokeUser)
}

// --- Handlers ---

func (s *Server) ListTenants(ctx context.Context, _ *mcpsdk.CallToolRequest, _ ListTenantsInput) (*mcpsdk.CallToolResult, any, error) {
	tenants, err := s.memory.ListTenants(ctx)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(tenants), nil, nil
}

func (s *Server) CreateTenant(ctx context.Context, _ *mcpsdk.CallToolRequest, input CreateTenantInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Name == "" || len(input.Name) > maxAdminFieldLen {
		return errorResult("name is required and must be <= 200 characters"), nil, nil
	}
	tenant, err := s.memory.CreateTenant(ctx, input.Name, input.Email)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(tenant), nil, nil
}

func (s *Server) UpdateTenant(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateTenantInput) (*mcpsdk.CallToolResult, any, error) {
	id, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if input.Name != nil && len(*input.Name) > maxAdminFieldLen {
		return errorResult("name must be <= 200 characters"), nil, nil
	}
	tenant, err := s.memory.UpdateTenant(ctx, id, service.UpdateTenantFields{
		Name:               input.Name,
		Email:              input.Email,
		StalenessMode:      input.StalenessMode,
		DuplicateGuard:     input.DuplicateGuard,
		CleanupScanEnabled: input.CleanupScanEnabled,
	})
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(tenant), nil, nil
}

func (s *Server) DeleteTenant(ctx context.Context, _ *mcpsdk.CallToolRequest, input DeleteTenantInput) (*mcpsdk.CallToolResult, any, error) {
	id, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if err := s.memory.DeleteTenant(ctx, id); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) CreateAPIKey(ctx context.Context, _ *mcpsdk.CallToolRequest, input CreateAPIKeyInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if input.Label == "" || len(input.Label) > maxAdminFieldLen {
		return errorResult("label is required and must be <= 200 characters"), nil, nil
	}
	var expiresAt *time.Time
	if input.ExpiresInDays != nil {
		if *input.ExpiresInDays <= 0 {
			return errorResult("expires_in_days must be > 0 when set"), nil, nil
		}
		t := time.Now().Add(time.Duration(*input.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}
	plaintext, key, err := s.memory.CreateAPIKey(ctx, tenantID, input.Label, input.SubjectID, expiresAt)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]any{
		"id":         key.ID,
		"tenant_id":  key.TenantID,
		"label":      key.Label,
		"prefix":     key.Prefix,
		"key":        plaintext,
		"expires_at": key.ExpiresAt,
	}), nil, nil
}

func (s *Server) ListAPIKeys(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListAPIKeysInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	keys, err := s.memory.ListAPIKeys(ctx, tenantID)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(keys), nil, nil
}

func (s *Server) RevokeAPIKey(ctx context.Context, _ *mcpsdk.CallToolRequest, input RevokeAPIKeyInput) (*mcpsdk.CallToolResult, any, error) {
	id, err := uuid.Parse(input.KeyID)
	if err != nil {
		return errorResult("invalid key_id: " + err.Error()), nil, nil
	}
	if err := s.memory.RevokeAPIKey(ctx, id); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{"status": "revoked"}), nil, nil
}

func (s *Server) RotateAPIKey(ctx context.Context, _ *mcpsdk.CallToolRequest, input RotateAPIKeyInput) (*mcpsdk.CallToolResult, any, error) {
	id, err := uuid.Parse(input.KeyID)
	if err != nil {
		return errorResult("invalid key_id: " + err.Error()), nil, nil
	}
	var grace time.Duration
	if input.GraceHours != nil {
		if *input.GraceHours < 0 {
			return errorResult("grace_hours must be >= 0"), nil, nil
		}
		grace = time.Duration(*input.GraceHours) * time.Hour
	}
	plaintext, key, err := s.memory.RotateAPIKey(ctx, id, grace)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]any{
		"id":        key.ID,
		"tenant_id": key.TenantID,
		"label":     key.Label,
		"prefix":    key.Prefix,
		"key":       plaintext,
	}), nil, nil
}

func (s *Server) GrantUser(ctx context.Context, _ *mcpsdk.CallToolRequest, input GrantUserInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	if input.Email == "" || len(input.Email) > maxAdminFieldLen {
		return errorResult("email is required and must be <= 200 characters"), nil, nil
	}
	tu, err := s.memory.GrantTenantUser(ctx, input.Email, tenantID, input.Role)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(tu), nil, nil
}

func (s *Server) ListUsers(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListUsersInput) (*mcpsdk.CallToolResult, any, error) {
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return errorResult("invalid tenant_id: " + err.Error()), nil, nil
	}
	users, err := s.memory.ListTenantUsers(ctx, tenantID)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(users), nil, nil
}

func (s *Server) UpdateUserRole(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateUserRoleInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Email == "" {
		return errorResult("email is required"), nil, nil
	}
	tu, err := s.memory.UpdateTenantUserRole(ctx, input.Email, input.Role)
	if err != nil {
		return handleAdminError(err)
	}
	return jsonResult(tu), nil, nil
}

func (s *Server) RevokeUser(ctx context.Context, _ *mcpsdk.CallToolRequest, input RevokeUserInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Email == "" {
		return errorResult("email is required"), nil, nil
	}
	if err := s.memory.RevokeTenantUser(ctx, input.Email); err != nil {
		return handleAdminError(err)
	}
	return jsonResult(map[string]string{"status": "revoked"}), nil, nil
}

// handleAdminError maps service errors to MCP results.
func handleAdminError(err error) (*mcpsdk.CallToolResult, any, error) {
	if errors.Is(err, apperr.ErrInvalidInput) || errors.Is(err, apperr.ErrNotFound) {
		return errorResult(err.Error()), nil, nil
	}
	return nil, nil, fmt.Errorf("admin operation: %w", err)
}
