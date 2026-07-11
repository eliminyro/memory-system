//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

func newAdminTestSvc(db *gorm.DB, store authz.Store) *service.MemoryService {
	return service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil,
		store,
	)
}

func requireNoTuple(t *testing.T, store authz.Store, notWant authz.Tuple) {
	t.Helper()
	got, err := store.ReadByObjectRelation(context.Background(), notWant.ObjectType, notWant.ObjectID, notWant.Relation)
	require.NoError(t, err)
	for _, g := range got {
		if g == notWant {
			t.Fatalf("tuple %+v should be absent; have %+v", notWant, got)
		}
	}
}

// TestUserRoleLifecycle covers grant -> role change -> revoke with authz-tuple
// sync at every step. The membership/admin tuples must track the tenant_users
// row exactly, or the JWT (human) path's Check drifts from the stored role.
func TestUserRoleLifecycle(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	// The offline-admin seam stands in for a privileged operator.
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(ctx, "roles-"+uuid.NewString(), "")
	require.NoError(t, err)

	email := "u-" + uuid.NewString() + "@example.com"
	tu, err := svc.GrantTenantUser(ctx, email, tenant.ID, "member")
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))
	requireNoTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))

	// list reflects the grant.
	users, err := svc.ListTenantUsers(ctx, tenant.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Equal(t, email, users[0].Email)

	// Duplicate email is refused.
	_, err = svc.GrantTenantUser(ctx, email, tenant.ID, "member")
	require.Error(t, err)

	// member -> admin adds the admin tuple.
	_, err = svc.UpdateTenantUserRole(ctx, email, "admin")
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))

	// admin -> member removes the admin tuple, keeps membership.
	_, err = svc.UpdateTenantUserRole(ctx, email, "member")
	require.NoError(t, err)
	requireNoTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))

	// revoke removes the row and its membership tuple.
	require.NoError(t, svc.RevokeTenantUser(ctx, email))
	requireNoTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))
	users, err = svc.ListTenantUsers(ctx, tenant.ID)
	require.NoError(t, err)
	require.Empty(t, users)
}

// TestKeyRotation covers immediate rotation (old revoked, new valid) and grace
// rotation (old stays valid until now+grace, new valid immediately).
func TestKeyRotation(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())
	validator := auth.NewAPIKeyValidator(db)

	tenant, err := svc.CreateTenant(ctx, "rotate-"+uuid.NewString(), "")
	require.NoError(t, err)

	// Immediate rotation.
	oldPlain, oldKey, err := svc.CreateAPIKey(ctx, tenant.ID, "k", nil, nil)
	require.NoError(t, err)
	newPlain, _, err := svc.RotateAPIKey(ctx, oldKey.ID, 0)
	require.NoError(t, err)
	if _, err := validator.ValidateKey(ctx, oldPlain); err == nil {
		t.Error("old key still valid after immediate rotation")
	}
	if _, err := validator.ValidateKey(ctx, newPlain); err != nil {
		t.Errorf("new key invalid after rotation: %v", err)
	}

	// Grace rotation: old stays valid, its expiry is set into the future.
	gracePlain, graceKey, err := svc.CreateAPIKey(ctx, tenant.ID, "g", nil, nil)
	require.NoError(t, err)
	grNewPlain, _, err := svc.RotateAPIKey(ctx, graceKey.ID, time.Hour)
	require.NoError(t, err)
	if _, err := validator.ValidateKey(ctx, gracePlain); err != nil {
		t.Errorf("graced old key rejected before window elapsed: %v", err)
	}
	if _, err := validator.ValidateKey(ctx, grNewPlain); err != nil {
		t.Errorf("new key invalid after grace rotation: %v", err)
	}
}

// TestAdminOpsFailClosedWithoutAdmin confirms admin ops deny a context that is
// neither local-admin nor a system-admin subject.
func TestAdminOpsFailClosedWithoutAdmin(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	// A bare context: no subject, no local-admin flag.
	_, err := svc.GrantTenantUser(context.Background(), "x@example.com", uuid.New(), "member")
	require.Error(t, err)
	_, err = svc.CreateTenant(context.Background(), "nope", "")
	require.Error(t, err)
}
