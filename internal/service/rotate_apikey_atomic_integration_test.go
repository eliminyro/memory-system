//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestRotateAPIKey_HappyPath: grace=0 rotation mints a valid replacement and
// revokes the predecessor, both committed together (F4).
func TestRotateAPIKey_HappyPath(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())
	validator := auth.NewAPIKeyValidator(db)

	tenant, err := svc.CreateTenant(ctx, "rot-atomic-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	oldPlain, oldKey, err := svc.CreateAPIKey(ctx, tenant.ID, "k", nil, nil)
	require.NoError(t, err)

	newPlain, newKey, err := svc.RotateAPIKey(ctx, oldKey.ID, 0)
	require.NoError(t, err)
	require.NotNil(t, newKey)
	require.NotEqual(t, oldKey.ID, newKey.ID)

	// New key valid, old key revoked.
	_, err = validator.ValidateKey(ctx, newPlain)
	require.NoError(t, err, "new key must be valid after rotation")
	_, err = validator.ValidateKey(ctx, oldPlain)
	require.Error(t, err, "old key must be revoked after immediate rotation")
}

// TestRotateAPIKey_RetireFailureRollsBack proves the F4 atomicity fix: when retiring
// the predecessor fails (here the old key is already revoked, so the in-tx Revoke
// matches no row and returns ErrNotFound), the whole rotation rolls back — no
// orphaned live replacement is committed. The tenant's key count is unchanged by the
// failed rotate.
func TestRotateAPIKey_RetireFailureRollsBack(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	tenant, err := svc.CreateTenant(ctx, "rot-rollback-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	_, oldKey, err := svc.CreateAPIKey(ctx, tenant.ID, "k", nil, nil)
	require.NoError(t, err)

	// Pre-revoke the predecessor so the in-tx Revoke will fail (already revoked).
	require.NoError(t, svc.RevokeAPIKey(ctx, oldKey.ID))

	before, err := svc.ListAPIKeys(ctx, tenant.ID)
	require.NoError(t, err)

	// Rotation must fail AND must not persist a new key.
	_, newKey, err := svc.RotateAPIKey(ctx, oldKey.ID, 0)
	require.Error(t, err, "rotation must fail when the predecessor cannot be retired")
	require.Nil(t, newKey)

	after, err := svc.ListAPIKeys(ctx, tenant.ID)
	require.NoError(t, err)
	require.Len(t, after, len(before), "a failed rotation must not leave an orphaned replacement key")
}
