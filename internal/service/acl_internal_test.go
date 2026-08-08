package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
)

// newACLUnitSvc builds a service wired only with an in-memory authz store — no
// database at all — enough to exercise the pure ceiling-decision logic
// (canGrantTenantRelation, CanManageTenant) in isolation. Mirrors
// newBootstrapUnitSvc in bootstrap_test.go.
func newACLUnitSvc(store authz.Store) *MemoryService {
	return NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
}

// TestCanGrantTenantRelation covers the grant-ceiling matrix (design.md §6):
// viewer/member require manager+; manager requires tenant#admin/system admin;
// a manager may never appoint another manager or admin.
func TestCanGrantTenantRelation(t *testing.T) {
	tenantID := uuid.New()

	t.Run("local admin bypasses every ceiling", func(t *testing.T) {
		svc := newACLUnitSvc(authz.NewMemoryStore())
		ctx := auth.WithLocalAdmin(context.Background())
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelMember))
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager))
	})

	t.Run("system admin subject can appoint managers via the system-edge derivation", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLUnitSvc(store)
		admin := "admin-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(admin)))
		require.NoError(t, store.Write(context.Background(), authzseed.TenantSystemEdge(tenantID)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: admin})
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager), "system admin must be able to appoint a manager")
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelMember))
	})

	t.Run("tenant admin can appoint managers", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLUnitSvc(store)
		subj := "adm-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantAdmin(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager), "tenant admin must be able to appoint a manager")
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelMember))
	})

	t.Run("manager can grant viewer/member but NOT manager", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLUnitSvc(store)
		subj := "mgr-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.True(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelMember))
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager), "a manager must NOT be able to appoint another manager")
	})

	t.Run("plain member cannot grant anything", func(t *testing.T) {
		store := authz.NewMemoryStore()
		svc := newACLUnitSvc(store)
		subj := "mem-" + uuid.NewString()
		require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenantID, subj)))
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelMember))
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager))
	})

	t.Run("subjectless denied", func(t *testing.T) {
		svc := newACLUnitSvc(authz.NewMemoryStore())
		ctx := context.Background()
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelViewer))
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, authz.RelManager))
	})

	t.Run("unknown relation always denies even for admin", func(t *testing.T) {
		svc := newACLUnitSvc(authz.NewMemoryStore())
		ctx := auth.WithLocalAdmin(context.Background())
		require.False(t, svc.canGrantTenantRelation(ctx, tenantID, "owner"))
	})
}

// TestTenantGrantTupleMapping proves each accepted relation maps to the
// matching authzseed constructor (design.md §2/§5).
func TestTenantGrantTupleMapping(t *testing.T) {
	tenantID := uuid.New()
	subj := uuid.NewString()
	require.Equal(t, authzseed.TenantViewer(tenantID, subj), tenantGrantTuple(tenantID, subj, authz.RelViewer))
	require.Equal(t, authzseed.TenantMember(tenantID, subj), tenantGrantTuple(tenantID, subj, authz.RelMember))
	require.Equal(t, authzseed.TenantManager(tenantID, subj), tenantGrantTuple(tenantID, subj, authz.RelManager))
}

// TestDocumentGrantTupleMapping proves each accepted relation maps to the
// matching authzseed constructor (design.md §2/§5).
func TestDocumentGrantTupleMapping(t *testing.T) {
	docID := uuid.New()
	subj := uuid.NewString()
	require.Equal(t, authzseed.DocumentViewer(docID, subj), documentGrantTuple(docID, subj, authz.RelViewer))
	require.Equal(t, authzseed.DocumentEditor(docID, subj), documentGrantTuple(docID, subj, authz.RelEditor))
}
