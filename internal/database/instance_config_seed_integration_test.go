//go:build integration

package database_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

// The env→DB seed must apply to a pre-existing (unseeded) row exactly once and
// never clobber a later admin edit — the regression the config-page change guards.
func TestMigrateSeedsGlobalsOnceOnExistingRow(t *testing.T) {
	db := openSwapPG(t)
	// This test mutates the shared instance_config singleton; drop it so the next
	// package's Migrate reseeds baseline (avoids cross-package pollution).
	t.Cleanup(func() { db.Exec(`DELETE FROM instance_config WHERE id = ?`, models.InstanceConfigSingletonID) })
	td := database.TenantColumnDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true}
	base := database.BaselineGlobalConfigDefaults()

	// Fresh install: the singleton is inserted seeded from env.
	require.NoError(t, db.Exec(`DELETE FROM instance_config WHERE id = ?`, models.InstanceConfigSingletonID).Error)
	gc1 := base
	gc1.CandidatePool = 33
	gc1.SelfServicePolicy = models.SelfServicePolicyAdminOnly
	require.NoError(t, database.Migrate(db, "fake", "fake", swapDim, td, gc1))

	var cfg models.InstanceConfig
	require.NoError(t, db.First(&cfg, "id = ?", models.InstanceConfigSingletonID).Error)
	require.Equal(t, 33, cfg.CandidatePool)
	require.Equal(t, models.SelfServicePolicyAdminOnly, cfg.SelfServicePolicy)
	require.True(t, cfg.GlobalsSeeded)

	// Simulate a row that predates this change: new columns at struct defaults, unseeded.
	require.NoError(t, db.Exec(
		`UPDATE instance_config SET candidate_pool = 20, self_service_policy = 'open', globals_seeded = false WHERE id = ?`,
		models.InstanceConfigSingletonID).Error)

	// Upgrade boot: the unseeded row is seeded from env (behavior preserved, not reset to defaults).
	gc2 := base
	gc2.CandidatePool = 50
	gc2.SelfServicePolicy = models.SelfServicePolicyAdminOnly
	require.NoError(t, database.Migrate(db, "fake", "fake", swapDim, td, gc2))
	require.NoError(t, db.First(&cfg, "id = ?", models.InstanceConfigSingletonID).Error)
	require.Equal(t, 50, cfg.CandidatePool, "env upgrade must seed the unseeded row")
	require.Equal(t, models.SelfServicePolicyAdminOnly, cfg.SelfServicePolicy, "self-service lock must not silently reopen")
	require.True(t, cfg.GlobalsSeeded)

	// An admin edit after seeding survives a later boot with a different env value.
	require.NoError(t, db.Exec(`UPDATE instance_config SET candidate_pool = 7 WHERE id = ?`, models.InstanceConfigSingletonID).Error)
	gc3 := base
	gc3.CandidatePool = 999
	require.NoError(t, database.Migrate(db, "fake", "fake", swapDim, td, gc3))
	require.NoError(t, db.First(&cfg, "id = ?", models.InstanceConfigSingletonID).Error)
	require.Equal(t, 7, cfg.CandidatePool, "a seeded row must not be re-clobbered by env")
}
