//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// TestACLTenantGrantLifecycle covers the delegated tenant-membership grant
// surface end to end: a manager (not a system admin, not the tenant's admin)
// can grant/revoke viewer, is denied appointing a manager (the ceiling), a
// system admin CAN appoint a manager, and an unknown email resolves not-found.
func TestACLTenantGrantLifecycle(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	sysAdminCtx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(sysAdminCtx, "acl-tenant-"+uuid.NewString(), "")
	require.NoError(t, err)

	// A manager subject: a direct tenant#manager tuple, no admin standing.
	managerSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenant.ID, managerSubj)))
	managerCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: managerSubj})

	// Target user must already have a tenant_users row; the ACL surface never
	// auto-creates one (design.md §3).
	targetEmail := "target-" + uuid.NewString() + "@example.com"
	targetTU, err := svc.GrantTenantUser(sysAdminCtx, targetEmail, tenant.ID, models.TenantUserRoleMember)
	require.NoError(t, err)

	// Manager grants viewer -> tuple written, visible via ListTenantGrants.
	require.NoError(t, svc.GrantTenantAccess(managerCtx, tenant.ID, targetEmail, authz.RelViewer))
	requireTuple(t, store, authzseed.TenantViewer(tenant.ID, targetTU.ID.String()))

	grants, err := svc.ListTenantGrants(managerCtx, tenant.ID)
	require.NoError(t, err)
	require.Contains(t, grants, service.Grant{Email: targetEmail, SubjectID: targetTU.ID.String(), Relation: authz.RelViewer})

	// Manager revokes viewer -> tuple gone, list no longer shows it.
	require.NoError(t, svc.RevokeTenantAccess(managerCtx, tenant.ID, targetEmail, authz.RelViewer))
	requireNoTuple(t, store, authzseed.TenantViewer(tenant.ID, targetTU.ID.String()))
	grants, err = svc.ListTenantGrants(managerCtx, tenant.ID)
	require.NoError(t, err)
	require.NotContains(t, grants, service.Grant{Email: targetEmail, SubjectID: targetTU.ID.String(), Relation: authz.RelViewer})

	// Manager attempting to appoint another manager is denied (the ceiling) —
	// no tuple results.
	err = svc.GrantTenantAccess(managerCtx, tenant.ID, targetEmail, authz.RelManager)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
	requireNoTuple(t, store, authzseed.TenantManager(tenant.ID, targetTU.ID.String()))

	// System admin CAN appoint a manager.
	require.NoError(t, svc.GrantTenantAccess(sysAdminCtx, tenant.ID, targetEmail, authz.RelManager))
	requireTuple(t, store, authzseed.TenantManager(tenant.ID, targetTU.ID.String()))

	// Unknown email resolves not-found, even for an otherwise-authorized caller.
	err = svc.GrantTenantAccess(managerCtx, tenant.ID, "no-such-user-"+uuid.NewString()+"@example.com", authz.RelViewer)
	require.ErrorIs(t, err, apperr.ErrNotFound)
}

// TestACLDocumentGrantLifecycle covers the per-document guest-sharing surface:
// a manager of the document's owning tenant can grant/revoke a guest
// viewer/editor grant, and a plain member of that tenant is denied.
func TestACLDocumentGrantLifecycle(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	sysAdminCtx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(sysAdminCtx, "acl-doc-tenant-"+uuid.NewString(), "")
	require.NoError(t, err)

	managerSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenant.ID, managerSubj)))
	managerCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: managerSubj})

	// Seed the document via an admin-scoped context (StoreDocument establishes
	// tenant from context, independent of the manager relation being tested).
	docCtx := auth.WithTenantID(sysAdminCtx, tenant.ID)
	res, err := svc.StoreDocument(docCtx, "learnings", nil, "acl-doc-"+uuid.NewString(), "# T\n\nbody", true, "seed", nil)
	require.NoError(t, err)
	docID := res.Document.ID

	targetEmail := "target-" + uuid.NewString() + "@example.com"
	targetTU, err := svc.GrantTenantUser(sysAdminCtx, targetEmail, tenant.ID, models.TenantUserRoleMember)
	require.NoError(t, err)

	// Manager (of the doc's owning tenant) grants a guest viewer.
	require.NoError(t, svc.GrantDocumentAccess(managerCtx, docID, targetEmail, authz.RelViewer))
	requireTuple(t, store, authzseed.DocumentViewer(docID, targetTU.ID.String()))

	grants, err := svc.ListDocumentGrants(managerCtx, docID)
	require.NoError(t, err)
	require.Contains(t, grants, service.Grant{Email: targetEmail, SubjectID: targetTU.ID.String(), Relation: authz.RelViewer})

	// M2 regression: archiving the document must NOT strand the lingering
	// guest tuple beyond the manager's reach. documentTenantID is deliberately
	// archived-inclusive so list/revoke still resolve the doc's tenant.
	require.NoError(t, db.Model(&models.Document{}).Where("id = ?", docID).
		Update("archived_at", time.Now()).Error)

	grants, err = svc.ListDocumentGrants(managerCtx, docID)
	require.NoError(t, err)
	require.Contains(t, grants, service.Grant{Email: targetEmail, SubjectID: targetTU.ID.String(), Relation: authz.RelViewer},
		"guest grant on an archived document must still be listable")

	// Revoke removes the tuple and drops it from the list, even while archived.
	require.NoError(t, svc.RevokeDocumentAccess(managerCtx, docID, targetEmail, authz.RelViewer))
	requireNoTuple(t, store, authzseed.DocumentViewer(docID, targetTU.ID.String()))
	grants, err = svc.ListDocumentGrants(managerCtx, docID)
	require.NoError(t, err)
	require.NotContains(t, grants, service.Grant{Email: targetEmail, SubjectID: targetTU.ID.String(), Relation: authz.RelViewer})

	// A plain member of the same tenant (not a manager) is denied.
	memberSubj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(tenant.ID, memberSubj)))
	memberCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: memberSubj})
	err = svc.GrantDocumentAccess(memberCtx, docID, targetEmail, authz.RelEditor)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
	requireNoTuple(t, store, authzseed.DocumentEditor(docID, targetTU.ID.String()))
}

// TestWritableTenantsIntegration proves the full WritableTenants resolution:
// a system admin sees every tenant (labeled admin), a manager sees only the
// tenant(s) they manage (labeled manager) and not tenants where they hold a
// lesser (member) relation, and a plain member sees nothing.
func TestWritableTenantsIntegration(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	sysAdminCtx := auth.WithLocalAdmin(context.Background())

	managed, err := svc.CreateTenant(sysAdminCtx, "wt-managed-"+uuid.NewString(), "")
	require.NoError(t, err)
	unmanaged, err := svc.CreateTenant(sysAdminCtx, "wt-unmanaged-"+uuid.NewString(), "")
	require.NoError(t, err)

	managerSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(managed.ID, managerSubj)))
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(unmanaged.ID, managerSubj)))
	managerCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: managerSubj})

	got, err := svc.WritableTenants(managerCtx)
	require.NoError(t, err)
	require.Len(t, got, 1, "must see exactly the managed tenant, not the member-only one")
	require.Equal(t, managed.ID, got[0].Tenant.ID)
	require.Equal(t, authz.RelManager, got[0].Relation)

	// System admin sees every tenant, labeled admin.
	all, err := svc.WritableTenants(sysAdminCtx)
	require.NoError(t, err)
	byID := make(map[uuid.UUID]string, len(all))
	for _, ta := range all {
		byID[ta.Tenant.ID] = ta.Relation
	}
	require.Equal(t, authz.RelAdmin, byID[managed.ID])
	require.Equal(t, authz.RelAdmin, byID[unmanaged.ID])

	// A plain member (no manager tuple anywhere) sees nothing.
	memberSubj := "mem-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(unmanaged.ID, memberSubj)))
	memberCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: memberSubj})
	none, err := svc.WritableTenants(memberCtx)
	require.NoError(t, err)
	require.Empty(t, none)
}
