//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestMigrateCoercesOffTenant proves the two-mode migration flips a legacy tenant
// row whose staleness_mode is 'off' to 'advisory', idempotently, on Migrate.
func TestMigrateCoercesOffTenant(t *testing.T) {
	db := openServicePG(t)
	id := uuid.New()
	require.NoError(t, db.Exec(
		`INSERT INTO tenants (id, name, staleness_mode, duplicate_guard, cleanup_scan_enabled, created_at, updated_at)
		 VALUES (?, ?, 'off', false, false, now(), now())`,
		id, "off-"+uuid.NewString()).Error)

	require.NoError(t, database.Migrate(db, "fake", "fake", fakeDim,
		database.TenantColumnDefaults{StalenessMode: models.StalenessModeAdvisory},
		database.BaselineGlobalConfigDefaults()))

	var tn models.Tenant
	require.NoError(t, db.First(&tn, "id = ?", id).Error)
	require.Equal(t, models.StalenessModeAdvisory, tn.StalenessMode,
		"legacy off tenant is migrated to advisory")
}

// TestTenantSettingsWriteCoercesOff proves an 'off' submitted to the tenant-settings
// write is coerced to advisory (the floor) rather than rejected or stored as off.
func TestTenantSettingsWriteCoercesOff(t *testing.T) {
	svc, adminCtx := newTenantDefaultsSvc(t, models.BaselineTenantDefaults())
	tenant, err := svc.CreateTenant(adminCtx, "coerce-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	off := models.StalenessModeOff
	updated, err := svc.UpdateTenantSettings(adminCtx, tenant.ID, &off, nil, nil, false, nil, nil)
	require.NoError(t, err, "off is coerced, not rejected")
	require.Equal(t, models.StalenessModeAdvisory, updated.StalenessMode)

	// Persisted: an admin read-back reflects the coerced value, never off.
	persisted, err := svc.UpdateTenantSettings(adminCtx, tenant.ID, nil, nil, nil, false, nil, nil)
	require.NoError(t, err)
	require.Equal(t, models.StalenessModeAdvisory, persisted.StalenessMode)
}
