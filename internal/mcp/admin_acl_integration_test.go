//go:build integration

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// newACLIntegrationServer wires a full MemoryService over the test database and
// returns the MCP Server, mirroring the construction in boundary_integration_test.go.
func newACLIntegrationServer(t *testing.T, db *gorm.DB, store authz.Store) *Server {
	t.Helper()
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(boundaryDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewPolicyStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		nil, nil,
		store,
	)
	return NewServer(svc, authz.NewEngine(store))
}

// firstText returns the text of a tool result's first content block.
func firstText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty tool result")
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("unexpected content type %T", res.Content[0])
	}
	return tc.Text
}

// TestMCPCreateTenantWithType_Persists proves create_tenant forwards the
// display type to the shared CreateTenant service method and it is persisted.
func TestMCPCreateTenantWithType_Persists(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	srv := newACLIntegrationServer(t, db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	res, _, err := srv.CreateTenant(ctx, nil, CreateTenantInput{
		Name:       "typed-" + uuid.NewString(),
		Type:       models.TenantTypePersonal,
		OwnerEmail: "owner-" + uuid.NewString() + "@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("create_tenant with a valid type must succeed, got %+v", res)
	}
	var got struct {
		ID   uuid.UUID `json:"id"`
		Type string    `json:"type"`
	}
	if err := json.Unmarshal([]byte(firstText(t, res)), &got); err != nil {
		t.Fatalf("decode tenant: %v", err)
	}
	if got.Type != models.TenantTypePersonal {
		t.Fatalf("persisted type = %q, want %q", got.Type, models.TenantTypePersonal)
	}
}

// TestMCPUpdateTenantType_Persists proves update_tenant forwards the type patch
// to UpdateTenantFields and it is persisted.
func TestMCPUpdateTenantType_Persists(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	srv := newACLIntegrationServer(t, db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := srv.memory.CreateTenant(ctx, "upd-"+uuid.NewString(), "")
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	personal := models.TenantTypePersonal

	res, _, err := srv.UpdateTenant(ctx, nil, UpdateTenantInput{
		TenantID: tenant.ID.String(),
		Type:     &personal,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("update_tenant with a valid type must succeed, got %+v", res)
	}
	var got struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(firstText(t, res)), &got); err != nil {
		t.Fatalf("decode tenant: %v", err)
	}
	if got.Type != models.TenantTypePersonal {
		t.Fatalf("persisted type = %q, want %q", got.Type, models.TenantTypePersonal)
	}
}

// TestMCPGrantTenantAccess_AdminWritesGrant proves grant_tenant_access is a
// thin delegator to the shared GrantTenantAccess: an admin grants a member and
// the tuple is written (visible via ListTenantGrants), no DB surgery.
func TestMCPGrantTenantAccess_AdminWritesGrant(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	srv := newACLIntegrationServer(t, db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := srv.memory.CreateTenant(ctx, "grant-"+uuid.NewString(), "")
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	email := "target-" + uuid.NewString() + "@example.com"
	if _, err := srv.memory.GrantTenantUser(ctx, email, tenant.ID, models.TenantUserRoleMember); err != nil {
		t.Fatalf("seed tenant_users row: %v", err)
	}

	res, _, err := srv.GrantTenantAccess(ctx, nil, GrantTenantAccessInput{
		TenantID: tenant.ID.String(),
		Email:    email,
		Relation: authz.RelMember,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("admin grant_tenant_access must succeed, got %+v", res)
	}

	grants, err := srv.memory.ListTenantGrants(ctx, tenant.ID)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	found := false
	for _, g := range grants {
		if g.Email == email && g.Relation == authz.RelMember {
			found = true
		}
	}
	if !found {
		t.Fatalf("granted member tuple not found in %+v", grants)
	}
}

// TestMCPCreateAPIKey_SharedRefused proves the MCP create_api_key tool inherits
// the service rule: minting a key for a shared tenant is refused as a tool error.
func TestMCPCreateAPIKey_SharedRefused(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	srv := newACLIntegrationServer(t, db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	shared, err := srv.memory.CreateTenant(ctx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	if err != nil {
		t.Fatalf("seed shared tenant: %v", err)
	}

	res, _, err := srv.CreateAPIKey(ctx, nil, CreateAPIKeyInput{
		TenantID: shared.ID.String(),
		Label:    "k",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("create_api_key on a shared tenant must be a tool error, got %+v", res)
	}
}

// TestMCPGrantTenantAccess_PersonalRefused proves the MCP grant_tenant_access
// tool inherits the service rule: a tenant-level grant into a personal tenant is
// refused as a tool error.
func TestMCPGrantTenantAccess_PersonalRefused(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	srv := newACLIntegrationServer(t, db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	personal, err := srv.memory.CreateTenant(ctx, "personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	if err != nil {
		t.Fatalf("seed personal tenant: %v", err)
	}

	res, _, err := srv.GrantTenantAccess(ctx, nil, GrantTenantAccessInput{
		TenantID: personal.ID.String(),
		Email:    "guest-" + uuid.NewString() + "@example.com",
		Relation: authz.RelViewer,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("grant_tenant_access into a personal tenant must be a tool error, got %+v", res)
	}
}
