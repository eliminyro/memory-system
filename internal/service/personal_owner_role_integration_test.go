//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/database"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestOwnerRoleTupleLifecycle covers grant(owner) -> role changes -> revoke with
// the authz-tuple sync at every step: the stored tuples must always match exactly
// one elevated role (owner XOR admin), with the member tuple untouched.
func TestOwnerRoleTupleLifecycle(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(ctx, "ownerlc-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	email := "olc-" + uuid.NewString() + "@example.com"
	tu, err := svc.GrantTenantUser(ctx, email, tenant.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))
	requireTuple(t, store, authzseed.TenantOwner(tenant.ID, tu.ID.String()))
	requireNoTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))

	// owner -> admin: admin tuple appears, owner tuple removed, member intact.
	_, err = svc.UpdateTenantUserRole(ctx, email, models.TenantUserRoleAdmin)
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))
	requireNoTuple(t, store, authzseed.TenantOwner(tenant.ID, tu.ID.String()))
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))

	// admin -> owner: owner tuple back, admin tuple removed.
	_, err = svc.UpdateTenantUserRole(ctx, email, models.TenantUserRoleOwner)
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantOwner(tenant.ID, tu.ID.String()))
	requireNoTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))

	// owner -> member: both elevated tuples gone, member intact.
	_, err = svc.UpdateTenantUserRole(ctx, email, models.TenantUserRoleMember)
	require.NoError(t, err)
	requireNoTuple(t, store, authzseed.TenantOwner(tenant.ID, tu.ID.String()))
	requireNoTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))

	// revoke removes the membership tuple.
	require.NoError(t, svc.RevokeTenantUser(ctx, email))
	requireNoTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))
}

// TestOwnerReadsAndAdministersOwnTenant proves an owner reads + administers their
// own personal tenant (owner ⇒ manager ⇒ member) while the existing no-leak
// property still holds: an owner reaches no other tenant.
func TestOwnerReadsAndAdministersOwnTenant(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(adminCtx, "own-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "o-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	ownerCtx := ctxFor(personal.ID, ownerTU.ID.String())

	// Owner stores + reads a doc in their own tenant, and manages the tenant.
	res, err := svc.StoreDocument(ownerCtx, "learnings", nil, "od-"+uuid.NewString(), "# T\n\n## H\nbody", true, "seed", nil, nil)
	require.NoError(t, err)
	_, err = svc.GetDocument(ownerCtx, res.Document.Category, nil, res.Document.Slug, false, "", nil)
	require.NoError(t, err, "owner reads own-tenant doc")
	require.True(t, svc.CanManageTenant(ownerCtx, personal.ID), "owner administers own tenant")

	// A second personal tenant with its own owner + doc.
	other, err := svc.CreateTenant(adminCtx, "other-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	otherTU, err := svc.GrantTenantUser(adminCtx, "x-"+uuid.NewString()+"@example.com", other.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	otherCtx := ctxFor(other.ID, otherTU.ID.String())
	ores, err := svc.StoreDocument(otherCtx, "learnings", nil, "xd-"+uuid.NewString(), "# T\n\n## H\nbody", true, "seed", nil, nil)
	require.NoError(t, err)

	// No leak: the first owner reaches nothing in the other tenant.
	require.False(t, svc.CanManageTenant(ownerCtx, other.ID), "owner cannot administer a foreign tenant")
	_, err = svc.GetDocument(ownerCtx, ores.Document.Category, nil, ores.Document.Slug, false, "", &other.ID)
	require.ErrorIs(t, err, apperr.ErrNotFound, "owner cannot read a foreign tenant's doc (no leak)")
}

// TestOwnerSelfServiceAPIKey covers create_api_key authorization under the owner
// role: an owner self-creates a key on their own personal tenant, cannot pin a
// foreign subject, and cannot create a key on a shared tenant — while a system
// admin retains full create + pin.
func TestOwnerSelfServiceAPIKey(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(adminCtx, "keyown-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "ko-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	ownerCtx := ctxFor(personal.ID, ownerTU.ID.String())

	// Owner self-creates a key on their own personal tenant. It is pinned to the
	// OWNER's subject, never the tenant service principal.
	_, key, err := svc.CreateAPIKey(ownerCtx, personal.ID, "self", nil, nil)
	require.NoError(t, err, "owner may self-create a key on their own personal tenant")
	require.NotNil(t, key)
	require.NotNil(t, key.SubjectID)
	require.Equal(t, ownerTU.ID.String(), *key.SubjectID, "owner key is scoped to the owner subject, not svc:<tenant>")

	// Escalation guard: seed a residual system#admin on the tenant service
	// principal (as a pre-owner-role deploy's seedAdminServicePrincipals would
	// have, when the owner was role=admin). An owner-minted key must NOT inherit
	// it — it is pinned to the owner subject, not svc:<tenant>.
	svcPrincipal := authz.ServicePrincipalID(personal.ID.String())
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(svcPrincipal)))
	require.True(t, svc.IsAdmin(ctxFor(personal.ID, svcPrincipal)), "precondition: svc principal carries the residual system admin")
	_, key2, err := svc.CreateAPIKey(ownerCtx, personal.ID, "self2", nil, nil)
	require.NoError(t, err)
	require.Equal(t, ownerTU.ID.String(), *key2.SubjectID, "owner key still scoped to the owner subject despite the residual svc admin")
	require.False(t, svc.IsAdmin(ctxFor(personal.ID, *key2.SubjectID)), "owner-minted key is not a system admin even when svc:<tenant> is")

	// Owner may NOT pin a key to an explicit foreign subject.
	foreign := authz.ServicePrincipalID(uuid.NewString())
	_, _, err = svc.CreateAPIKey(ownerCtx, personal.ID, "pin", &foreign, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "a non-system-admin owner may not pin a foreign subject")

	// Owner has no rights on a shared tenant.
	shared, err := svc.CreateTenant(adminCtx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	_, _, err = svc.CreateAPIKey(ownerCtx, shared.ID, "x", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "owner cannot create a key on a shared tenant")

	// System admin behavior unchanged: create + pin a foreign subject on a personal tenant.
	pinned := authz.ServicePrincipalID(uuid.NewString())
	_, pkey, err := svc.CreateAPIKey(adminCtx, personal.ID, "admin-pin", &pinned, nil)
	require.NoError(t, err, "system admin retains full pinning")
	require.NotNil(t, pkey)
}

// TestOwnerManagesDocGuestButNotTenantGrant proves an owner may grant/revoke
// per-document guest access on their personal tenant (via owner ⇒ manager ⇒
// CanManageTenant) but cannot widen tenant-level access (personal tenants reject
// tenant-level grants regardless of the caller's role).
func TestOwnerManagesDocGuestButNotTenantGrant(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(adminCtx, "docown-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "do-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	ownerCtx := ctxFor(personal.ID, ownerTU.ID.String())

	res, err := svc.StoreDocument(ownerCtx, "learnings", nil, "dg-"+uuid.NewString(), "# T\n\n## H\nbody", true, "seed", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID

	// A guest email needs a tenant_users row somewhere so it resolves; put it on a
	// shared tenant (admin-created).
	shared, err := svc.CreateTenant(adminCtx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	guestEmail := "guest-" + uuid.NewString() + "@example.com"
	guestTU, err := svc.GrantTenantUser(adminCtx, guestEmail, shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)

	// Owner grants + revokes a doc-guest read on their own doc.
	require.NoError(t, svc.GrantDocumentAccess(ownerCtx, docID, guestEmail, authz.RelViewer),
		"owner may grant a doc-guest on their personal tenant's doc")
	requireTuple(t, store, authzseed.DocumentViewer(docID, guestTU.ID.String()))
	require.NoError(t, svc.RevokeDocumentAccess(ownerCtx, docID, guestEmail, authz.RelViewer))
	requireNoTuple(t, store, authzseed.DocumentViewer(docID, guestTU.ID.String()))

	// Owner cannot widen tenant-level access: a personal tenant rejects tenant-level grants.
	err = svc.GrantTenantAccess(ownerCtx, personal.ID, guestEmail, authz.RelViewer)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "owner cannot GrantTenantAccess on a personal tenant")
}

// TestPersonalAdminBackfillsToOwner proves the boot-time migration converts a
// pre-owner-role personal admin (role + tuple) to owner, leaves shared tenants
// untouched, and is a no-op on re-run.
func TestPersonalAdminBackfillsToOwner(t *testing.T) {
	db := openServicePG(t) // runs Migrate once on the (empty) schema
	store := authz.NewPostgresStore(db)
	ctx := context.Background()

	// A personal tenant whose sole user is a legacy role=admin with a tenant#admin tuple.
	personal := models.Tenant{ID: uuid.New(), Name: "legacy-" + uuid.NewString(), Type: models.TenantTypePersonal}
	require.NoError(t, db.Create(&personal).Error)
	pUser := models.TenantUser{ID: uuid.New(), Email: "legacy-" + uuid.NewString() + "@example.com", TenantID: personal.ID, Role: models.TenantUserRoleAdmin}
	require.NoError(t, db.Create(&pUser).Error)
	require.NoError(t, store.Write(ctx, authzseed.TenantAdmin(personal.ID, pUser.ID.String())))

	// A shared tenant with a legacy role=admin user + tuple (must stay admin).
	shared := models.Tenant{ID: uuid.New(), Name: "legacy-shared-" + uuid.NewString(), Type: models.TenantTypeShared}
	require.NoError(t, db.Create(&shared).Error)
	sUser := models.TenantUser{ID: uuid.New(), Email: "legacy-shared-" + uuid.NewString() + "@example.com", TenantID: shared.ID, Role: models.TenantUserRoleAdmin}
	require.NoError(t, db.Create(&sUser).Error)
	require.NoError(t, store.Write(ctx, authzseed.TenantAdmin(shared.ID, sUser.ID.String())))

	assertConverted := func() {
		t.Helper()
		var gotP models.TenantUser
		require.NoError(t, db.First(&gotP, "id = ?", pUser.ID).Error)
		require.Equal(t, models.TenantUserRoleOwner, gotP.Role, "personal admin role must flip to owner")
		requireTuple(t, store, authzseed.TenantOwner(personal.ID, pUser.ID.String()))
		requireNoTuple(t, store, authzseed.TenantAdmin(personal.ID, pUser.ID.String()))

		var gotS models.TenantUser
		require.NoError(t, db.First(&gotS, "id = ?", sUser.ID).Error)
		require.Equal(t, models.TenantUserRoleAdmin, gotS.Role, "shared admin role must be untouched")
		requireTuple(t, store, authzseed.TenantAdmin(shared.ID, sUser.ID.String()))
		requireNoTuple(t, store, authzseed.TenantOwner(shared.ID, sUser.ID.String()))
	}

	// Run the migration (owner backfill + idempotent authz backfill).
	require.NoError(t, database.Migrate(db, "fake", "fake", fakeDim, database.TenantColumnDefaults{StalenessMode: "off"}))
	assertConverted()

	// Re-run is a no-op: still owner on personal, still admin on shared.
	require.NoError(t, database.Migrate(db, "fake", "fake", fakeDim, database.TenantColumnDefaults{StalenessMode: "off"}))
	assertConverted()
}

// TestResidualSvcSystemAdminCleanup proves the migration revokes a residual
// system#admin on a personal tenant's service principal (left by a pre-owner-role
// seedAdminServicePrincipals) UNLESS an API key still resolves to that svc
// principal (the founding/bootstrap admin key), which is preserved.
func TestResidualSvcSystemAdminCleanup(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	ctx := context.Background()

	// Founding-like: residual svc admin + a nil-subject key that resolves to svc (preserve).
	founding := models.Tenant{ID: uuid.New(), Name: "founding-" + uuid.NewString(), Type: models.TenantTypePersonal}
	require.NoError(t, db.Create(&founding).Error)
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(authz.ServicePrincipalID(founding.ID.String()))))
	require.NoError(t, db.Create(&models.APIKey{ID: uuid.New(), TenantID: founding.ID, KeyHash: "kh-" + uuid.NewString(), Label: "bootstrap", Prefix: "boot"}).Error) // SubjectID nil ⇒ svc

	// Ordinary auto-provisioned: residual svc admin, NO svc-resolving key (delete).
	ordinary := models.Tenant{ID: uuid.New(), Name: "ordinary-" + uuid.NewString(), Type: models.TenantTypePersonal}
	require.NoError(t, db.Create(&ordinary).Error)
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(authz.ServicePrincipalID(ordinary.ID.String()))))

	assertCleaned := func() {
		t.Helper()
		requireTuple(t, store, authzseed.SystemAdmin(authz.ServicePrincipalID(founding.ID.String())))   // key backs it → preserved
		requireNoTuple(t, store, authzseed.SystemAdmin(authz.ServicePrincipalID(ordinary.ID.String()))) // dangling → deleted
	}

	require.NoError(t, database.Migrate(db, "fake", "fake", fakeDim, database.TenantColumnDefaults{StalenessMode: "off"}))
	assertCleaned()
	// Idempotent.
	require.NoError(t, database.Migrate(db, "fake", "fake", fakeDim, database.TenantColumnDefaults{StalenessMode: "off"}))
	assertCleaned()
}
