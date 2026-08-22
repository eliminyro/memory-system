package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/service"
)

// newACLNoDBSvc mirrors newBootstrapUnitSvc: an authz store only, no database
// — enough to exercise CanManageTenant, WritableTenants' cheap branches, and
// every ceiling/validation rejection that must short-circuit before ever
// touching the database.
func newACLNoDBSvc(store authz.Store) *service.MemoryService {
	return service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
}

func TestCanManageTenant(t *testing.T) {
	tenantID := uuid.New()

	t.Run("local admin", func(t *testing.T) {
		svc := newACLNoDBSvc(authz.NewMemoryStore())
		require.True(t, svc.CanManageTenant(auth.WithLocalAdmin(context.Background()), tenantID))
	})

	t.Run("tenant manager", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLNoDBSvc(store)
		subj := "mgr-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.True(t, svc.CanManageTenant(ctx, tenantID))
	})

	t.Run("tenant admin (derived manager)", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLNoDBSvc(store)
		subj := "adm-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantAdmin(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.True(t, svc.CanManageTenant(ctx, tenantID))
	})

	t.Run("plain member denied", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLNoDBSvc(store)
		subj := "mem-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.False(t, svc.CanManageTenant(ctx, tenantID))
	})

	t.Run("subjectless denied", func(t *testing.T) {
		svc := newACLNoDBSvc(authz.NewMemoryStore())
		require.False(t, svc.CanManageTenant(context.Background(), tenantID))
	})
}

// --- Unknown-relation rejection: must reject before any database access. ---

func TestGrantTenantAccessRejectsUnknownRelation(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	err := svc.GrantTenantAccess(auth.WithLocalAdmin(context.Background()), uuid.New(), "user@example.com", "owner")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

func TestRevokeTenantAccessRejectsUnknownRelation(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	err := svc.RevokeTenantAccess(auth.WithLocalAdmin(context.Background()), uuid.New(), "user@example.com", "owner")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

func TestGrantDocumentAccessRejectsUnknownRelation(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	err := svc.GrantDocumentAccess(auth.WithLocalAdmin(context.Background()), uuid.New(), "user@example.com", "commenter")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

func TestRevokeDocumentAccessRejectsUnknownRelation(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	err := svc.RevokeDocumentAccess(auth.WithLocalAdmin(context.Background()), uuid.New(), "user@example.com", "commenter")
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestGrantTenantAccessCeilingDeniesBeforeTouchingDB proves the ceiling check
// runs BEFORE the email lookup: a plain tenant member (not a manager) is
// denied with no database wired at all. If the implementation ever reordered
// this to resolve the email first, this test would panic on the nil db
// instead of returning a clean error.
func TestGrantTenantAccessCeilingDeniesBeforeTouchingDB(t *testing.T) {
	store := authz.NewMemoryStore()
	tenantID := uuid.New()
	subj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
	svc := newACLNoDBSvc(store)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	err := svc.GrantTenantAccess(ctx, tenantID, "user@example.com", authz.RelViewer)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestGrantTenantAccessManagerCannotAppointManager proves the manager-ceiling
// rule through the exported entry point, again with no database wired: a
// manager trying to grant "manager" is denied before any DB touch.
func TestGrantTenantAccessManagerCannotAppointManager(t *testing.T) {
	store := authz.NewMemoryStore()
	tenantID := uuid.New()
	subj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenantID, subj)))
	svc := newACLNoDBSvc(store)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	err := svc.GrantTenantAccess(ctx, tenantID, "user@example.com", authz.RelManager)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestRevokeTenantAccessCeilingDeniesBeforeTouchingDB mirrors the grant-side
// test for the revoke path.
func TestRevokeTenantAccessCeilingDeniesBeforeTouchingDB(t *testing.T) {
	store := authz.NewMemoryStore()
	tenantID := uuid.New()
	subj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
	svc := newACLNoDBSvc(store)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	err := svc.RevokeTenantAccess(ctx, tenantID, "user@example.com", authz.RelViewer)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

func TestListTenantGrantsDeniesWithoutManagerCeiling(t *testing.T) {
	store := authz.NewMemoryStore()
	tenantID := uuid.New()
	subj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
	svc := newACLNoDBSvc(store)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	_, err := svc.ListTenantGrants(ctx, tenantID)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestWritableTenantsNoCandidates proves a subject holding only non-manager
// tenant tuples yields an empty list without ever touching the tenants
// repository (nil in this fixture) — the manager Check fails for the
// candidate before any tenants.GetByID call would be reached.
func TestWritableTenantsNoCandidates(t *testing.T) {
	store := authz.NewMemoryStore()
	tenantID := uuid.New()
	subj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
	svc := newACLNoDBSvc(store)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	got, err := svc.WritableTenants(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWritableTenantsSubjectlessReturnsEmpty(t *testing.T) {
	svc := newACLNoDBSvc(authz.NewMemoryStore())
	got, err := svc.WritableTenants(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWritableTenantsNoAuthzStoreReturnsEmpty(t *testing.T) {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: "x"})
	got, err := svc.WritableTenants(ctx)
	require.NoError(t, err)
	require.Empty(t, got)
}
