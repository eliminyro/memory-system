//go:build integration

package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// seedFoundingAdmin marks the instance bootstrapped for the auto-provision
// precondition (PR-A4): ProvisionPersonalTenant now fails closed until a
// system#admin tuple exists. A single throwaway subject is enough for HasAnyAdmin
// to report the instance as bootstrapped.
func seedFoundingAdmin(t *testing.T, store authz.Store) {
	t.Helper()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin("svc:founding-"+uuid.NewString())))
}

// TestCreateTenantTypeDefaultAndExplicit proves CreateTenant defaults to shared
// and persists an explicit personal type (group 2).
func TestCreateTenantTypeDefaultAndExplicit(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	def, err := svc.CreateTenant(ctx, "type-default-"+uuid.NewString(), "")
	require.NoError(t, err)
	require.Equal(t, models.TenantTypeShared, def.Type, "no type arg must default to shared")

	pers, err := svc.CreateTenant(ctx, "type-personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	require.Equal(t, models.TenantTypePersonal, pers.Type)

	// Admin update can change the type (validated).
	updated, err := svc.UpdateTenant(ctx, def.ID, service.UpdateTenantFields{Type: strPtr(models.TenantTypePersonal)})
	require.NoError(t, err)
	require.Equal(t, models.TenantTypePersonal, updated.Type)
}

// TestProvisionPersonalTenantGatePass provisions a personal tenant for a
// gate-passing verified email and makes the email tenant#admin (task 5.1/5.3).
func TestProvisionPersonalTenantGatePass(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	seedFoundingAdmin(t, store)

	email := "prov-" + uuid.NewString() + "@example.com"
	id, err := svc.ProvisionPersonalTenant(context.Background(), email, "", "", []string{"example.com"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	tid, err := uuid.Parse(id)
	require.NoError(t, err)

	// The tenant is personal.
	adminCtx := auth.WithLocalAdmin(context.Background())
	users, err := svc.ListTenantUsers(adminCtx, tid)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, email, users[0].Email)

	// The email's subject (tenant_users.id) is tenant#admin of the new tenant.
	requireTuple(t, store, authzseed.TenantAdmin(tid, users[0].ID.String()))

	// No public-read wildcard on a personal tenant (default-pool-only).
	requireNoTuple(t, store, authzseed.TenantViewer(tid, authz.Wildcard))
}

// TestProvisionPersonalTenantRequiresFoundingAdmin proves the bootstrap gate
// (PR-A4) against a real DB: on an un-bootstrapped instance a gate-passing login
// is refused and NO tenant_users row is persisted (the collision source);
// once a founding admin exists, the same login auto-provisions as before.
func TestProvisionPersonalTenantRequiresFoundingAdmin(t *testing.T) {
	db := openServicePG(t)
	// system:memory#admin is a global singleton (not tenant-scoped), and the
	// integration suite shares one DB with no per-test truncation, so sibling
	// provision tests that seedFoundingAdmin leave admin tuples behind. Clear them
	// so this test's pre-bootstrap phase starts genuinely un-bootstrapped; Cleanup
	// re-runs the delete so the founding admin seeded below does not leak.
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	email := "gate-boot-" + uuid.NewString() + "@example.com"

	// Un-bootstrapped: no system#admin yet -> fail closed, create nothing.
	id, err := svc.ProvisionPersonalTenant(context.Background(), email, "", "", []string{"example.com"})
	require.ErrorIs(t, err, service.ErrSignupNotAllowed)
	require.Empty(t, id)

	var rows int64
	require.NoError(t, db.Model(&models.TenantUser{}).Where("email = ?", email).Count(&rows).Error)
	require.Zero(t, rows, "no tenant_users row may be created before bootstrap (the /bootstrap collision source)")

	// After bootstrap: a founding admin exists -> auto-provision resumes.
	seedFoundingAdmin(t, store)
	id, err = svc.ProvisionPersonalTenant(context.Background(), email, "", "", []string{"example.com"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	require.NoError(t, db.Model(&models.TenantUser{}).Where("email = ?", email).Count(&rows).Error)
	require.Equal(t, int64(1), rows, "exactly one tenant_users row after a post-bootstrap provision")
}

// TestProvisionPersonalTenantReturningEmail proves a second provision of the
// same email returns the existing tenant, never a second one.
func TestProvisionPersonalTenantReturningEmail(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	seedFoundingAdmin(t, store)

	email := "returning-" + uuid.NewString() + "@example.com"
	first, err := svc.ProvisionPersonalTenant(context.Background(), email, "", "", nil)
	require.NoError(t, err)

	second, err := svc.ProvisionPersonalTenant(context.Background(), email, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, first, second, "returning email must resolve to the same tenant")
}

// TestProvisionPersonalTenantRace runs concurrent first-logins of the same
// email; the unique constraints + read-after-write must yield exactly one
// tenant (every caller returns the same id).
func TestProvisionPersonalTenantRace(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	seedFoundingAdmin(t, store)

	email := "race-" + uuid.NewString() + "@example.com"
	const n = 6
	var wg sync.WaitGroup
	ids := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = svc.ProvisionPersonalTenant(context.Background(), email, "", "", nil)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, ids[0], ids[i], "all concurrent provisions must resolve to one tenant")
	}
}

// TestProvisionPersonalTenantUsesDisplayName proves the display name (OIDC `name`
// claim) is the tenant name base when provided, not the email local-part.
func TestProvisionPersonalTenantUsesDisplayName(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	seedFoundingAdmin(t, store)

	display := "Ada Lovelace " + uuid.NewString()
	email := "ada-" + uuid.NewString() + "@example.com"
	id, err := svc.ProvisionPersonalTenant(context.Background(), email, display, "", nil)
	require.NoError(t, err)

	var tn models.Tenant
	require.NoError(t, db.First(&tn, "id = ?", id).Error)
	require.Equal(t, display, tn.Name, "display name must be the tenant name base")
	require.Equal(t, models.TenantTypePersonal, tn.Type)
}

// TestProvisionPersonalTenantDistinctEmailsSameBaseName proves two different
// verified emails that share a base name BOTH provision, to distinct tenants:
// the second is disambiguated by email domain rather than failing on the
// globally-unique tenants.name index.
func TestProvisionPersonalTenantDistinctEmailsSameBaseName(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	seedFoundingAdmin(t, store)

	base := "John Smith " + uuid.NewString()
	emailA := "a-" + uuid.NewString() + "@alpha.example"
	emailB := "b-" + uuid.NewString() + "@bravo.example"

	idA, err := svc.ProvisionPersonalTenant(context.Background(), emailA, base, "", nil)
	require.NoError(t, err)
	idB, err := svc.ProvisionPersonalTenant(context.Background(), emailB, base, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, idA, idB, "distinct emails sharing a base name must get distinct tenants")

	var tnA, tnB models.Tenant
	require.NoError(t, db.First(&tnA, "id = ?", idA).Error)
	require.NoError(t, db.First(&tnB, "id = ?", idB).Error)
	require.Equal(t, base, tnA.Name, "first claimant keeps the bare base name")
	require.Equal(t, base+" (bravo.example)", tnB.Name, "second claimant is disambiguated by email domain")
}

// TestListTenantsByTypeAdminAndManager covers the list surface (group 7): a
// system admin sees tenants filtered by type and narrowed by q; a non-admin
// manager sees only the tenants they manage.
func TestListTenantsByTypeAdminAndManager(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	tag := uuid.NewString()
	personal, err := svc.CreateTenant(adminCtx, "list-personal-"+tag, "", models.TenantTypePersonal)
	require.NoError(t, err)
	shared, err := svc.CreateTenant(adminCtx, "list-shared-"+tag, "", models.TenantTypeShared)
	require.NoError(t, err)

	// Admin, type=personal, q narrows to the tagged personal tenant.
	got, err := svc.ListTenantsByType(adminCtx, models.TenantTypePersonal, "list-personal-"+tag)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, personal.ID, got[0].Tenant.ID)
	require.Equal(t, authz.RelAdmin, got[0].Relation)

	// Admin, exact-UUID q resolves the shared tenant.
	gotUUID, err := svc.ListTenantsByType(adminCtx, models.TenantTypeShared, shared.ID.String())
	require.NoError(t, err)
	require.Len(t, gotUUID, 1)
	require.Equal(t, shared.ID, gotUUID[0].Tenant.ID)

	// Wrong-type filter excludes it.
	none, err := svc.ListTenantsByType(adminCtx, models.TenantTypePersonal, shared.ID.String())
	require.NoError(t, err)
	require.Empty(t, none)

	// A non-admin manager sees only the tenant they manage.
	mgrSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(shared.ID, mgrSubj)))
	mgrCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: mgrSubj})
	mgrGot, err := svc.ListTenantsByType(mgrCtx, models.TenantTypeShared, "")
	require.NoError(t, err)
	require.Len(t, mgrGot, 1)
	require.Equal(t, shared.ID, mgrGot[0].Tenant.ID)
	require.Equal(t, authz.RelManager, mgrGot[0].Relation)
}
