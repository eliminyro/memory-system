//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestGrantTenantUser_PersonalTenantSingleOwner locks the personal-tenant
// single-user guard: the first grant succeeds, a second grant of a different
// email to the same personal tenant is rejected with ErrInvalidInput. The
// sequential case trips the pre-tx count fast-fail; the concurrent case (two
// grants both reading count=0) is closed by the FOR UPDATE re-count now taken
// inside the autocommit transaction (no unique constraint on tenant_users).
func TestGrantTenantUser_PersonalTenantSingleOwner(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(ctx, "psingle-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	_, err = svc.GrantTenantUser(ctx, "a-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.NoError(t, err, "the first user on a personal tenant is allowed")

	_, err = svc.GrantTenantUser(ctx, "b-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleOwner)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "a second user on a personal tenant must be rejected")
}
