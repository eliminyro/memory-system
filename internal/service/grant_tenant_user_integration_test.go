//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
)

// failWriteStore wraps a real store but forces every tuple Write to fail. Reads
// still delegate so unrelated setup (e.g. requireAdmin / lifecycle reads) works.
type failWriteStore struct {
	authz.Store
}

func (failWriteStore) Write(context.Context, authz.Tuple) error {
	return errors.New("forced authz write failure")
}

// TestGrantTenantUser_AuthzWriteFailureRollsBack guards B11: on the direct
// autocommit path a failed access-tuple write used to be logged-and-swallowed,
// leaving a committed tenant_users row with NO membership tuple (no access) while
// returning success. The grant is now all-or-nothing: the tuple write hard-fails,
// the row rolls back, and the error propagates.
func TestGrantTenantUser_AuthzWriteFailureRollsBack(t *testing.T) {
	db := openServicePG(t)
	store := failWriteStore{authz.NewPostgresStore(db)}
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	// Shared tenant (default) so the personal single-owner rule doesn't interfere.
	// CreateTenant only seeds tuples best-effort, so the failing store doesn't block
	// setup.
	tenant, err := svc.CreateTenant(ctx, "b11-"+uuid.NewString(), "")
	require.NoError(t, err)

	email := "b11-" + uuid.NewString() + "@example.com"
	_, err = svc.GrantTenantUser(ctx, email, tenant.ID, models.TenantUserRoleMember)
	require.Error(t, err, "a failed access-tuple write must surface, not be swallowed")

	var n int64
	require.NoError(t, db.Model(&models.TenantUser{}).Where("email = ?", email).Count(&n).Error)
	require.Equal(t, int64(0), n, "no tenant_users row may be committed without its access tuple")
}
