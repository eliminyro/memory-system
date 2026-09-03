package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/service"
)

// newNoDBToolServer builds a Server backed by a service with only an authz
// store wired (no database), mirroring the service package's newACLNoDBSvc.
// It is enough to exercise every validation / grant-ceiling rejection that
// must short-circuit before any database access — the same no-DB surface the
// service unit tests use.
func newNoDBToolServer(store authz.Store) *Server {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
	return &Server{memory: svc}
}

func memberCtx(store authz.Store, tenantID uuid.UUID) (context.Context, error) {
	subj := "mem-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)); err != nil {
		return nil, err
	}
	return auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}), nil
}

// TestMCPGrantTenantAccess_BadRelation proves the tool delegates relation
// validation to the service: an unknown relation is rejected (invalid input)
// before any database access.
func TestMCPGrantTenantAccess_BadRelation(t *testing.T) {
	srv := newNoDBToolServer(authz.NewMemoryStore())
	ctx := auth.WithLocalAdmin(context.Background())

	res, _, err := srv.GrantTenantAccess(ctx, nil, GrantTenantAccessInput{
		TenantID: uuid.NewString(),
		Email:    "a@example.com",
		Relation: "owner",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("bad relation must be a tool error, got %+v", res)
	}
}

// TestMCPGrantTenantAccess_NonManagerRefused proves the grant-ceiling is
// inherited from the service, identical to the /api/acl surface: a plain
// member (not a manager) is refused, with no database wired.
func TestMCPGrantTenantAccess_NonManagerRefused(t *testing.T) {
	store := authz.NewMemoryStore()
	srv := newNoDBToolServer(store)
	tenantID := uuid.New()
	ctx, err := memberCtx(store, tenantID)
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}

	res, _, err := srv.GrantTenantAccess(ctx, nil, GrantTenantAccessInput{
		TenantID: tenantID.String(),
		Email:    "a@example.com",
		Relation: authz.RelViewer,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("non-manager grant must be refused (ceiling), got %+v", res)
	}
}

// TestMCPGrantDocumentAccess_BadRelation proves the document surface's relation
// set (viewer/editor) is enforced by the service — "manager" is rejected
// before any database access.
func TestMCPGrantDocumentAccess_BadRelation(t *testing.T) {
	srv := newNoDBToolServer(authz.NewMemoryStore())
	ctx := auth.WithLocalAdmin(context.Background())

	res, _, err := srv.GrantDocumentAccess(ctx, nil, GrantDocumentAccessInput{
		DocumentID: uuid.NewString(),
		Email:      "a@example.com",
		Relation:   authz.RelManager,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("bad document relation must be a tool error, got %+v", res)
	}
}

// TestMCPGrantTenantAccess_InvalidID proves a malformed tenant_id is rejected
// as a tool error before the service is called.
func TestMCPGrantTenantAccess_InvalidID(t *testing.T) {
	srv := newNoDBToolServer(authz.NewMemoryStore())
	res, _, err := srv.GrantTenantAccess(auth.WithLocalAdmin(context.Background()), nil, GrantTenantAccessInput{
		TenantID: "not-a-uuid",
		Email:    "a@example.com",
		Relation: authz.RelViewer,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("invalid tenant_id must be a tool error, got %+v", res)
	}
}

// TestMCPCreateTenant_InvalidType proves the shared service type validation is
// reused: an unknown type is rejected before any database access.
func TestMCPCreateTenant_InvalidType(t *testing.T) {
	srv := newNoDBToolServer(authz.NewMemoryStore())
	ctx := auth.WithLocalAdmin(context.Background())

	res, _, err := srv.CreateTenant(ctx, nil, CreateTenantInput{
		Name: "acme",
		Type: "bogus",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("invalid tenant type must be a tool error, got %+v", res)
	}
}

// TestMCPUpdateTenant_InvalidType proves the same shared validation on the
// update path: an unknown type is rejected before the tenant row is read.
func TestMCPUpdateTenant_InvalidType(t *testing.T) {
	srv := newNoDBToolServer(authz.NewMemoryStore())
	ctx := auth.WithLocalAdmin(context.Background())
	bogus := "bogus"

	res, _, err := srv.UpdateTenant(ctx, nil, UpdateTenantInput{
		TenantID: uuid.NewString(),
		Type:     &bogus,
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("invalid tenant type must be a tool error, got %+v", res)
	}
}
