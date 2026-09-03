//go:build integration

package globalconfig_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/globalconfig"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/pgnotify"
	"github.com/eliminyro/memory-system/internal/repository"
)

const invDim = 768

func openInvPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, "fake", "fake", invDim,
		database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestConfigInvalidation_WriteObservedByListener proves a write on one connection
// converges a second cache via the trigger + listener, and that write-through
// stays independent of the listener.
func TestConfigInvalidation_WriteObservedByListener(t *testing.T) {
	db := openInvPG(t)
	repo := repository.NewInstanceConfigRepository(db)
	gc := globalconfig.New(repo)
	require.NoError(t, gc.Load(context.Background()))

	orig := gc.CandidatePool()
	t.Cleanup(func() { _ = repo.Update(context.Background(), models.InstanceConfigPatch{CandidatePool: &orig}) })

	sqlDB, err := db.DB()
	require.NoError(t, err)
	l := pgnotify.New(sqlDB, slog.New(slog.NewTextHandler(io.Discard, nil)),
		pgnotify.WithBackoff(20*time.Millisecond, 100*time.Millisecond))
	l.Register(models.InstanceConfigNotifyChannel, gc.Load)
	require.NoError(t, l.Start(context.Background()))
	require.Eventually(t, l.Healthy, 3*time.Second, 20*time.Millisecond)

	// Another connection's write fires the trigger; the listener reloads gc.
	observed := orig + 7
	require.NoError(t, repo.Update(context.Background(), models.InstanceConfigPatch{CandidatePool: &observed}))
	require.Eventually(t, func() bool { return gc.CandidatePool() == observed }, 3*time.Second, 20*time.Millisecond)

	// Write-through is independent: with the listener stopped, Refresh still
	// reflects the write immediately.
	l.Stop()
	direct := observed + 3
	require.NoError(t, repo.Update(context.Background(), models.InstanceConfigPatch{CandidatePool: &direct}))
	require.NoError(t, gc.Refresh(context.Background()))
	require.Equal(t, direct, gc.CandidatePool())
}
