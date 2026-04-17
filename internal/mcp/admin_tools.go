package mcp

import (
	"context"
	"errors"
	"fmt"

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
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to create key for"`
	Label    string `json:"label" jsonschema:"Key label (required, max 200 chars)"`
}

type ListAPIKeysInput struct {
	TenantID string `json:"tenant_id" jsonschema:"Tenant UUID to list keys for"`
}

type RevokeAPIKeyInput struct {
	KeyID string `json:"key_id" jsonschema:"API key UUID to revoke"`
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
	plaintext, key, err := s.memory.CreateAPIKey(ctx, tenantID, input.Label)
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

// handleAdminError maps service errors to MCP results.
func handleAdminError(err error) (*mcpsdk.CallToolResult, any, error) {
	if errors.Is(err, apperr.ErrInvalidInput) || errors.Is(err, apperr.ErrNotFound) {
		return errorResult(err.Error()), nil, nil
	}
	return nil, nil, fmt.Errorf("admin operation: %w", err)
}
