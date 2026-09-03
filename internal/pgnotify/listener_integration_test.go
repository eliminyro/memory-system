//go:build integration

package pgnotify_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/pgnotify"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func notify(t *testing.T, db *gorm.DB, channel string) {
	t.Helper()
	require.NoError(t, db.Exec("SELECT pg_notify(?, '')", channel).Error)
}

func newListener(t *testing.T, db *gorm.DB) *pgnotify.Listener {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	return pgnotify.New(sqlDB, quietLogger(), pgnotify.WithBackoff(20*time.Millisecond, 100*time.Millisecond))
}

func TestListener_RegisteredChannelReloads(t *testing.T) {
	db := openPG(t)
	var count atomic.Int64
	l := newListener(t, db)
	l.Register("cfgtest_a", func(context.Context) error { count.Add(1); return nil })
	require.NoError(t, l.Start(context.Background()))
	defer l.Stop()
	require.Eventually(t, l.Healthy, 3*time.Second, 20*time.Millisecond)

	base := count.Load()
	notify(t, db, "cfgtest_a")
	require.Eventually(t, func() bool { return count.Load() > base }, 3*time.Second, 20*time.Millisecond)
}

func TestListener_UnregisteredChannelIgnored(t *testing.T) {
	db := openPG(t)
	var count atomic.Int64
	l := newListener(t, db)
	l.Register("cfgtest_a", func(context.Context) error { count.Add(1); return nil })
	require.NoError(t, l.Start(context.Background()))
	defer l.Stop()
	require.Eventually(t, l.Healthy, 3*time.Second, 20*time.Millisecond)

	base := count.Load()
	notify(t, db, "cfgtest_unregistered")
	require.Never(t, func() bool { return count.Load() != base }, 500*time.Millisecond, 50*time.Millisecond)
	require.True(t, l.Healthy(), "listener stayed healthy after an unregistered notification")
}

func TestListener_OneConnectionServesEveryChannel(t *testing.T) {
	db := openPG(t)
	var a, b atomic.Int64
	l := newListener(t, db)
	l.Register("cfgtest_a", func(context.Context) error { a.Add(1); return nil })
	l.Register("cfgtest_b", func(context.Context) error { b.Add(1); return nil })
	require.NoError(t, l.Start(context.Background()))
	defer l.Stop()
	require.Eventually(t, l.Healthy, 3*time.Second, 20*time.Millisecond)

	var backends int
	require.NoError(t, db.Raw(
		"SELECT count(*) FROM pg_stat_activity WHERE application_name = ?", pgnotify.AppName,
	).Scan(&backends).Error)
	require.Equal(t, 1, backends, "the listener holds exactly one connection for all channels")

	ba, bb := a.Load(), b.Load()
	notify(t, db, "cfgtest_a")
	notify(t, db, "cfgtest_b")
	require.Eventually(t, func() bool { return a.Load() > ba && b.Load() > bb }, 3*time.Second, 20*time.Millisecond)
}

func TestListener_ReconnectReloadsEverything(t *testing.T) {
	db := openPG(t)
	var reloads atomic.Int64
	l := newListener(t, db)
	l.Register("cfgtest_a", func(context.Context) error { reloads.Add(1); return nil })
	require.NoError(t, l.Start(context.Background()))
	defer l.Stop()
	require.Eventually(t, func() bool { return l.Healthy() && reloads.Load() >= 1 }, 3*time.Second, 20*time.Millisecond)

	// Kill the listener's backend; a change made while it is down emits no
	// notification it can receive, so only the reconnect reload recovers it.
	require.NoError(t, db.Exec(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = ?", pgnotify.AppName,
	).Error)
	require.Eventually(t, func() bool { return l.Healthy() && reloads.Load() >= 2 }, 5*time.Second, 20*time.Millisecond)
}

func TestListener_ChangeInStartupWindowNotLost(t *testing.T) {
	db := openPG(t)
	var count atomic.Int64
	l := newListener(t, db)
	l.Register("cfgtest_a", func(context.Context) error { count.Add(1); return nil })
	// Start returns once LISTEN is established; a notification fired right after
	// is queued on the connection and picked up, so it cannot be lost.
	require.NoError(t, l.Start(context.Background()))
	defer l.Stop()

	base := count.Load()
	notify(t, db, "cfgtest_a")
	require.Eventually(t, func() bool { return count.Load() > base }, 3*time.Second, 20*time.Millisecond)
}
