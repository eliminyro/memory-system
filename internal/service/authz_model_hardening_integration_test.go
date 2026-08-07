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
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// TestUpdateMyTenantSettingsRequiresManager locks the F1 hardening: the ctx-only
// self-service toggle path requires MANAGE rights, not bare membership. A plain
// member of a SHARED tenant can no longer arm the destructive retention sweep
// (staleness_mode="hard"); a manager can; and a personal tenant's owner keeps
// self-service (owner ⇒ manager) so the fix does not regress personal use.
func TestUpdateMyTenantSettingsRequiresManager(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store) // global default "" ⇒ open
	adminCtx := auth.WithLocalAdmin(context.Background())

	shared, err := svc.CreateTenant(adminCtx, "harden-shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	// A plain member of a shared tenant is refused.
	memberTU, err := svc.GrantTenantUser(adminCtx, "mem-"+uuid.NewString()+"@example.com", shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	_, err = svc.UpdateMyTenantSettings(ctxFor(shared.ID, memberTU.ID.String()), strPtr(models.StalenessModeHard), nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "a shared tenant's plain member must be refused")

	// The refused edit left staleness_mode unchanged — the sweep was NOT armed.
	adminRead := auth.WithTenantID(auth.WithLocalAdmin(context.Background()), shared.ID)
	cur, err := svc.UpdateMyTenantSettings(adminRead, nil, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, models.StalenessModeHard, cur.StalenessMode, "a refused member edit must not arm hard retention")

	// A manager (direct tenant#manager tuple) may edit.
	managerSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(shared.ID, managerSubj)))
	got, err := svc.UpdateMyTenantSettings(ctxFor(shared.ID, managerSubj), strPtr(models.StalenessModeHard), nil, nil)
	require.NoError(t, err, "a manager may edit toggles")
	require.Equal(t, models.StalenessModeHard, got.StalenessMode)

	// Regression: a personal tenant's owner keeps self-service (owner ⇒ manager).
	personal, err := svc.CreateTenant(adminCtx, "harden-personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	ownerTU, err := svc.GrantTenantUser(adminCtx, "own-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err)
	pgot, err := svc.UpdateMyTenantSettings(ctxFor(personal.ID, ownerTU.ID.String()), strPtr(models.StalenessModeHard), nil, nil)
	require.NoError(t, err, "a personal tenant's owner keeps self-service")
	require.Equal(t, models.StalenessModeHard, pgot.StalenessMode)
}

// TestCreateAPIKeyRejectsWildcardSubject locks the F2b hardening: the only
// reachable write path that could seed a user:* tuple refuses a wildcard pin.
func TestCreateAPIKeyRejectsWildcardSubject(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	// API keys are a personal-tenant affordance.
	personal, err := svc.CreateTenant(adminCtx, "key-wild-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	_, _, err = svc.CreateAPIKey(adminCtx, personal.ID, "wild", strPtr(authz.Wildcard), nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, `subject_id="*" must be rejected`)

	// No key was persisted.
	keys, err := svc.ListAPIKeys(adminCtx, personal.ID)
	require.NoError(t, err)
	require.Empty(t, keys, "a rejected wildcard key must not be persisted")
}

// TestListTenantGrantsIncludesWildcard locks the F2c change: a public wildcard
// viewer grant is surfaced as an auditable entry (with a recognizable label),
// not silently dropped from the ACL listing.
func TestListTenantGrantsIncludesWildcard(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	adminCtx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(adminCtx, "wild-grants-"+uuid.NewString(), "")
	require.NoError(t, err)

	// Seed a public wildcard viewer grant directly.
	require.NoError(t, store.Write(context.Background(), authzseed.TenantViewer(tenant.ID, authz.Wildcard)))

	// A manager lists grants and must see the wildcard as an auditable entry.
	managerSubj := "mgr-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantManager(tenant.ID, managerSubj)))
	managerCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: managerSubj})

	grants, err := svc.ListTenantGrants(managerCtx, tenant.ID)
	require.NoError(t, err)
	require.Contains(t, grants, service.Grant{
		Email:     "(public wildcard)",
		SubjectID: authz.Wildcard,
		Relation:  authz.RelViewer,
	}, "wildcard grant must appear as an auditable entry, not be silently dropped")
}
