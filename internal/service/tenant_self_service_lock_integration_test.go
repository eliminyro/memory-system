//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// newSelfServiceSvc is newAdminTestSvc with an explicit global self-service
// default, so tests can exercise inherit-from-global resolution.
func newSelfServiceSvc(db *gorm.DB, store authz.Store, globalDefault string) *service.MemoryService {
	svc := newAdminTestSvc(db, store)
	svc.SelfServicePolicyDefault = globalDefault
	return svc
}

// TestSelfServicePolicyResolutionAndObservability proves effective-policy
// resolution (unset inherits the global default; a per-tenant override wins) and
// that reads surface both the stored value and the resolved effective policy.
func TestSelfServicePolicyResolutionAndObservability(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newSelfServiceSvc(db, store, models.SelfServicePolicyAdminOnly) // global admin_only
	adminCtx := auth.WithLocalAdmin(context.Background())

	inherit, err := svc.CreateTenant(adminCtx, "ssp-inherit-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	require.Nil(t, inherit.SelfServicePolicy, "fresh tenant carries no override")
	require.Equal(t, models.SelfServicePolicyAdminOnly, inherit.EffectiveSelfServicePolicy(svc.SelfServicePolicyDefault))

	override, err := svc.CreateTenant(adminCtx, "ssp-override-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	upd, err := svc.UpdateTenant(adminCtx, override.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr(models.SelfServicePolicyOpen)})
	require.NoError(t, err)
	require.NotNil(t, upd.SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyOpen, *upd.SelfServicePolicy)

	tenants, err := svc.ListTenants(adminCtx)
	require.NoError(t, err)
	byID := map[uuid.UUID]models.Tenant{}
	for _, tn := range tenants {
		byID[tn.ID] = tn
	}
	require.Nil(t, byID[inherit.ID].SelfServicePolicy, "stored override is null (inherit)")
	require.Equal(t, models.SelfServicePolicyAdminOnly, byID[inherit.ID].EffectivePolicy, "inherit resolves to the global default")
	require.Equal(t, models.SelfServicePolicyOpen, *byID[override.ID].SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyOpen, byID[override.ID].EffectivePolicy, "override wins over the global default")
}

// TestSelfServiceToggleEditMatrix covers UpdateMyTenantSettings across the
// open/locked × role matrix: open requires a manager (a plain member is refused —
// these toggles arm destructive retention); admin_only excludes the personal
// owner (owner ⇏ admin) but admits system admins, and on a shared tenant admits
// its tenant-admin while excluding a plain member.
func TestSelfServiceToggleEditMatrix(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store) // global default "" ⇒ open
	adminCtx := auth.WithLocalAdmin(context.Background())

	// open: a manager edits; a plain member is now refused (toggles are
	// manager-level — they arm destructive retention).
	shared, err := svc.CreateTenant(adminCtx, "tog-open-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	memberTU, err := svc.GrantTenantUser(adminCtx, "m-"+uuid.NewString()+"@example.com", shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(shared.ID, memberTU.ID.String()), strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "open: a plain member may NOT edit toggles (manager required)")

	openAdminTU, err := svc.GrantTenantUser(adminCtx, "oa-"+uuid.NewString()+"@example.com", shared.ID, models.TenantUserRoleAdmin)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(shared.ID, openAdminTU.ID.String()), strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.NoError(t, err, "open: a manager (tenant-admin) may edit toggles")

	// admin_only on a personal tenant: owner blocked, system admin allowed.
	personal, err := svc.CreateTenant(adminCtx, "tog-lock-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	_, err = svc.UpdateTenant(adminCtx, personal.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr(models.SelfServicePolicyAdminOnly)})
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "o-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(personal.ID, ownerTU.ID.String()), strPtr(models.StalenessModeHard), nil, nil, false, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "admin_only: personal owner blocked from editing toggles")

	sysCtx := auth.WithTenantID(auth.WithLocalAdmin(context.Background()), personal.ID)
	_, err = svc.UpdateMyTenantSettings(sysCtx, strPtr(models.StalenessModeHard), nil, nil, false, nil)
	require.NoError(t, err, "admin_only: a system admin may still edit toggles")

	// admin_only on a shared tenant: tenant-admin allowed, plain member blocked.
	sharedLocked, err := svc.CreateTenant(adminCtx, "tog-slock-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	_, err = svc.UpdateTenant(adminCtx, sharedLocked.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr(models.SelfServicePolicyAdminOnly)})
	require.NoError(t, err)
	adminTU, err := svc.GrantTenantUser(adminCtx, "sa-"+uuid.NewString()+"@example.com", sharedLocked.ID, models.TenantUserRoleAdmin)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(sharedLocked.ID, adminTU.ID.String()), strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.NoError(t, err, "admin_only: a shared tenant-admin may edit toggles")

	memTU, err := svc.GrantTenantUser(adminCtx, "sm-"+uuid.NewString()+"@example.com", sharedLocked.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(sharedLocked.ID, memTU.ID.String()), strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "admin_only: a plain member is blocked")
}

// TestSelfServiceAPIKeyMatrix covers CreateAPIKey under the policy: open lets an
// owner self-create (pinned to the owner subject); admin_only blocks the personal
// owner but not a system admin; the personal-tenant-only guard holds throughout.
func TestSelfServiceAPIKeyMatrix(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store) // open
	adminCtx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(adminCtx, "key-open-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "ko-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	ownerCtx := ctxFor(personal.ID, ownerTU.ID.String())

	// open: owner self-creates a key pinned to their OWN subject (not svc:<tenant>).
	_, key, err := svc.CreateAPIKey(ownerCtx, personal.ID, "self", nil, nil)
	require.NoError(t, err, "open: owner self-creates a key")
	require.NotNil(t, key.SubjectID)
	require.Equal(t, ownerTU.ID.String(), *key.SubjectID, "owner key is pinned to the owner subject")

	// admin_only: owner blocked, system admin still allowed.
	_, err = svc.UpdateTenant(adminCtx, personal.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr(models.SelfServicePolicyAdminOnly)})
	require.NoError(t, err)
	_, _, err = svc.CreateAPIKey(ownerCtx, personal.ID, "blocked", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "admin_only: owner blocked from key creation")
	_, akey, err := svc.CreateAPIKey(adminCtx, personal.ID, "admin", nil, nil)
	require.NoError(t, err, "admin_only: a system admin may still create a key")
	require.NotNil(t, akey)

	// Personal-only guard intact: even a system admin cannot key a shared tenant.
	shared, err := svc.CreateTenant(adminCtx, "key-shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	_, _, err = svc.CreateAPIKey(adminCtx, shared.ID, "x", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "personal-only guard: no keys on shared tenants")
}

// TestSelfServicePolicyAdminOnlySetter proves the override is set/cleared only via
// the admin-only UpdateTenant path (inherit clears to NULL; bad values rejected),
// and that a self-service toggle edit never mutates the policy.
func TestSelfServicePolicyAdminOnlySetter(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store) // open
	adminCtx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(adminCtx, "set-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	upd, err := svc.UpdateTenant(adminCtx, tenant.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr(models.SelfServicePolicyAdminOnly)})
	require.NoError(t, err)
	require.NotNil(t, upd.SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyAdminOnly, *upd.SelfServicePolicy)

	upd, err = svc.UpdateTenant(adminCtx, tenant.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr("inherit")})
	require.NoError(t, err)
	require.Nil(t, upd.SelfServicePolicy, "inherit clears the override to NULL")

	_, err = svc.UpdateTenant(adminCtx, tenant.ID, service.UpdateTenantFields{SelfServicePolicy: strPtr("locked")})
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "unknown policy value rejected")

	// The self-service path has no policy field: editing toggles (open ⇒ allowed)
	// must leave the stored override untouched.
	ownerTU, err := svc.GrantTenantUser(adminCtx, "so-"+uuid.NewString()+"@example.com", tenant.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(tenant.ID, ownerTU.ID.String()), strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.NoError(t, err)
	tenants, err := svc.ListTenants(adminCtx)
	require.NoError(t, err)
	for _, tn := range tenants {
		if tn.ID == tenant.ID {
			require.Nil(t, tn.SelfServicePolicy, "a self-service toggle edit must not set a policy override")
		}
	}
}

// TestSelfServiceDefaultOpenRegression proves that with no config and no override
// an owner edits toggles and self-creates a key exactly as before this change.
func TestSelfServiceDefaultOpenRegression(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store) // no config: global default "" ⇒ open
	adminCtx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(adminCtx, "reg-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	require.Nil(t, personal.SelfServicePolicy, "no override by default")
	ownerTU, err := svc.GrantTenantUser(adminCtx, "ro-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	ownerCtx := ctxFor(personal.ID, ownerTU.ID.String())

	_, err = svc.UpdateMyTenantSettings(ownerCtx, strPtr(models.StalenessModeAdvisory), nil, nil, false, nil)
	require.NoError(t, err, "default-open: owner edits toggles")
	_, key, err := svc.CreateAPIKey(ownerCtx, personal.ID, "self", nil, nil)
	require.NoError(t, err, "default-open: owner self-creates a key")
	require.NotNil(t, key.SubjectID)
	require.Equal(t, ownerTU.ID.String(), *key.SubjectID)
}
