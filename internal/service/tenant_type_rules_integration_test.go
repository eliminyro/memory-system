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
)

// TestCreateAPIKeyTenantTypeRule proves the shared service rule: API keys are a
// personal-tenant affordance. A shared tenant is refused; a personal one is
// allowed (the same path the bootstrap founding key takes).
func TestCreateAPIKeyTenantTypeRule(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	shared, err := svc.CreateTenant(ctx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	_, _, err = svc.CreateAPIKey(ctx, shared.ID, "k", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "API key on a shared tenant must be refused")

	personal, err := svc.CreateTenant(ctx, "personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	_, key, err := svc.CreateAPIKey(ctx, personal.ID, "k", nil, nil)
	require.NoError(t, err, "API key on a personal tenant must be allowed")
	require.NotNil(t, key)
}

// TestGrantTenantUserPersonalSingleOwner proves a personal tenant admits exactly
// one owner: the creation-time first user is allowed, a second is refused. Shared
// tenants remain unrestricted.
func TestGrantTenantUserPersonalSingleOwner(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	personal, err := svc.CreateTenant(ctx, "personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)

	// First (and only) owner: no prior tenant_users row -> allowed.
	_, err = svc.GrantTenantUser(ctx, "owner-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleAdmin)
	require.NoError(t, err, "first owner into a fresh personal tenant must be allowed")

	// Second user into the same personal tenant -> refused.
	_, err = svc.GrantTenantUser(ctx, "second-"+uuid.NewString()+"@example.com", personal.ID, models.TenantUserRoleMember)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "second user into a personal tenant must be refused")

	// Shared tenant: two distinct users both succeed.
	shared, err := svc.CreateTenant(ctx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	_, err = svc.GrantTenantUser(ctx, "a-"+uuid.NewString()+"@example.com", shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	_, err = svc.GrantTenantUser(ctx, "b-"+uuid.NewString()+"@example.com", shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err, "shared tenants take unlimited members")
}

// TestGrantTenantAccessTenantTypeRule proves a tenant-level ACL grant is refused
// on a personal tenant but allowed on a shared one, while a document-level guest
// grant on a personal tenant's document stays allowed.
func TestGrantTenantAccessTenantTypeRule(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	ctx := auth.WithLocalAdmin(context.Background())

	// Tenant-level grant into a personal tenant is refused (the personal guard
	// fires before the email is even resolved).
	personal, err := svc.CreateTenant(ctx, "personal-"+uuid.NewString(), "", models.TenantTypePersonal)
	require.NoError(t, err)
	err = svc.GrantTenantAccess(ctx, personal.ID, "guest-"+uuid.NewString()+"@example.com", authz.RelViewer)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "tenant-level grant into a personal tenant must be refused")

	// Tenant-level grant into a shared tenant succeeds (email must resolve first).
	shared, err := svc.CreateTenant(ctx, "shared-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	memberEmail := "member-" + uuid.NewString() + "@example.com"
	memberTU, err := svc.GrantTenantUser(ctx, memberEmail, shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	require.NoError(t, svc.GrantTenantAccess(ctx, shared.ID, memberEmail, authz.RelViewer),
		"tenant-level grant into a shared tenant must be allowed")
	requireTuple(t, store, authzseed.TenantViewer(shared.ID, memberTU.ID.String()))

	// Document-level guest grant on a personal tenant's document stays allowed.
	docCtx := auth.WithTenantID(ctx, personal.ID)
	res, err := svc.StoreDocument(docCtx, "learnings", nil, "ttr-doc-"+uuid.NewString(), "# T\n\nbody", true, "seed", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID

	// The guest needs a tenant_users row somewhere so the email resolves; use the
	// shared tenant for that.
	guestEmail := "docguest-" + uuid.NewString() + "@example.com"
	guestTU, err := svc.GrantTenantUser(ctx, guestEmail, shared.ID, models.TenantUserRoleMember)
	require.NoError(t, err)
	require.NoError(t, svc.GrantDocumentAccess(ctx, docID, guestEmail, authz.RelViewer),
		"document-level guest grant on a personal tenant's doc must be allowed")
	requireTuple(t, store, authzseed.DocumentViewer(docID, guestTU.ID.String()))
}
