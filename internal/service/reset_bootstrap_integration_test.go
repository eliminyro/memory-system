//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/service"
)

// TestResetBootstrap proves the break-glass reset (design D5, spec: *Break-glass
// reset*) is scoped to the admin key + system:memory#admin tuple(s) only: it
// bootstraps an instance, layers on unrelated tenant data (a second tenant, a
// document under it, and a non-admin API key), resets, then asserts the admin
// set is gone while every other row survives untouched.
func TestResetBootstrap(t *testing.T) {
	db := openServicePG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)
	svc.BootstrapToken = "s3cr3t"

	_, adminKey, err := svc.Bootstrap(context.Background(), "s3cr3t", service.BootstrapSpec{
		TenantName: "admin-" + uuid.NewString(),
	})
	require.NoError(t, err)
	require.NotNil(t, adminKey)

	has, err := svc.HasAnyAdmin(context.Background())
	require.NoError(t, err)
	require.True(t, has, "instance must be bootstrapped before reset")

	adminCtx := auth.WithLocalAdmin(context.Background())

	// Unrelated tenant data that must survive reset byte-for-byte: a second
	// tenant, a document under it, and a non-admin API key.
	tenant, err := svc.CreateTenant(adminCtx, "tenant-"+uuid.NewString(), "")
	require.NoError(t, err)
	docCtx := auth.WithTenantID(adminCtx, tenant.ID)
	storeRes, err := svc.StoreDocument(docCtx, "learnings", nil, "keep-"+uuid.NewString(), "# Title\n\nbody", true, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, storeRes.Document)

	_, memberKey, err := svc.CreateAPIKey(adminCtx, tenant.ID, "member", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, memberKey)

	var tenantCountBefore, docCountBefore, keyCountBefore int64
	require.NoError(t, db.Table("tenants").Count(&tenantCountBefore).Error)
	require.NoError(t, db.Table("documents").Count(&docCountBefore).Error)
	require.NoError(t, db.Table("api_keys").Count(&keyCountBefore).Error)

	require.NoError(t, svc.ResetBootstrap(context.Background()))

	has, err = svc.HasAnyAdmin(context.Background())
	require.NoError(t, err)
	require.False(t, has, "reset must clear the admin set")
	require.Empty(t, systemAdminTuples(t, store), "no system#admin tuple should remain after reset")

	// The admin key row is gone (hard-deleted, not merely revoked).
	var count int64
	require.NoError(t, db.Table("api_keys").Where("id = ?", adminKey.ID).Count(&count).Error)
	require.Zero(t, count, "admin key row must be deleted by reset")

	// Every other row is untouched: counts unchanged, and each specific fixture
	// row (bootstrap tenant, second tenant, document, non-admin key) still exists.
	var tenantCountAfter, docCountAfter, keyCountAfter int64
	require.NoError(t, db.Table("tenants").Count(&tenantCountAfter).Error)
	require.NoError(t, db.Table("documents").Count(&docCountAfter).Error)
	require.NoError(t, db.Table("api_keys").Count(&keyCountAfter).Error)
	require.Equal(t, tenantCountBefore, tenantCountAfter, "tenant count must be unchanged by reset")
	require.Equal(t, docCountBefore, docCountAfter, "document count must be unchanged by reset")
	require.Equal(t, keyCountBefore-1, keyCountAfter, "only the admin key should be removed from api_keys")

	require.NoError(t, db.Table("tenants").Where("id = ?", adminKey.TenantID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the bootstrap tenant itself must survive reset")

	require.NoError(t, db.Table("tenants").Where("id = ?", tenant.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the unrelated tenant must survive reset")

	require.NoError(t, db.Table("documents").Where("id = ?", storeRes.Document.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the unrelated document must survive reset")

	require.NoError(t, db.Table("api_keys").Where("id = ?", memberKey.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the non-admin key must survive reset")
}
