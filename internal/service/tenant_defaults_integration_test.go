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
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// newTenantDefaultsSvc builds a service with the given creation defaults plus an
// admin context, reusing the shared PG harness (openServicePG / fakeDim).
func newTenantDefaultsSvc(t *testing.T, defaults models.TenantDefaults) (*service.MemoryService, context.Context) {
	t.Helper()
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil, nil,
		store,
	)
	svc.TenantDefaults = defaults

	adminSubj := "admin-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(adminSubj)))
	adminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: adminSubj})
	return svc, adminCtx
}

// TestCreateTenantAppliesConfiguredDefaults guards the bug where GORM emitted the
// model's struct-tag defaults ('off'/false/false) on insert, bypassing the DB
// column default, so every tenant created through the service landed on 'off'
// regardless of the operator's configured bundle. All three create paths
// (CreateTenant, Bootstrap, ProvisionPersonalTenant) funnel through CreateTenant.
func TestCreateTenantAppliesConfiguredDefaults(t *testing.T) {
	t.Run("baseline bundle is stamped onto a new tenant", func(t *testing.T) {
		svc, adminCtx := newTenantDefaultsSvc(t, models.BaselineTenantDefaults())
		tenant, err := svc.CreateTenant(adminCtx, "baseline-"+uuid.NewString(), "", models.TenantTypeShared)
		require.NoError(t, err)
		require.Equal(t, models.StalenessModeHard, tenant.StalenessMode)
		require.True(t, tenant.DuplicateGuard)
		require.True(t, tenant.CleanupScanEnabled)
	})

	t.Run("operator override passes through verbatim", func(t *testing.T) {
		want := models.TenantDefaults{
			StalenessMode:      models.StalenessModeAdvisory,
			DuplicateGuard:     true,
			CleanupScanEnabled: false,
		}
		svc, adminCtx := newTenantDefaultsSvc(t, want)
		tenant, err := svc.CreateTenant(adminCtx, "override-"+uuid.NewString(), "", models.TenantTypeShared)
		require.NoError(t, err)
		require.Equal(t, want.StalenessMode, tenant.StalenessMode)
		require.Equal(t, want.DuplicateGuard, tenant.DuplicateGuard)
		require.Equal(t, want.CleanupScanEnabled, tenant.CleanupScanEnabled)
	})

	t.Run("unset defaults leave the tenant on the model default (upgrade-safe)", func(t *testing.T) {
		svc, adminCtx := newTenantDefaultsSvc(t, models.TenantDefaults{}) // zero value: not wired
		tenant, err := svc.CreateTenant(adminCtx, "unset-"+uuid.NewString(), "", models.TenantTypeShared)
		require.NoError(t, err)
		require.Equal(t, models.StalenessModeOff, tenant.StalenessMode)
		require.False(t, tenant.DuplicateGuard)
		require.False(t, tenant.CleanupScanEnabled)
	})
}
