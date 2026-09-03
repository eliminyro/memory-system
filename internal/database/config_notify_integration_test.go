//go:build integration

package database_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

const notifyDim = 768

func openNotifyPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, "fake", "fake", notifyDim,
		database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// listenConn opens a dedicated pgx connection LISTENing on the instance_config
// channel, cleaned up with the test.
func listenConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), "LISTEN "+models.InstanceConfigNotifyChannel)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// gotNotify reports whether a notification on the config channel arrives within d.
func gotNotify(t *testing.T, conn *pgx.Conn, d time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		t.Fatalf("wait for notification: %v", err)
	}
	return n.Channel == models.InstanceConfigNotifyChannel
}

// bumpConfig fires an UPDATE that changes nothing meaningful (only updated_at),
// so the AFTER trigger runs without perturbing values other tests assert on.
func bumpConfig(tx *gorm.DB) error {
	return tx.Exec("UPDATE instance_config SET updated_at = now() WHERE id = ?",
		models.InstanceConfigSingletonID).Error
}

func TestInstanceConfigTrigger_CommitEmitsAtCommit(t *testing.T) {
	db := openNotifyPG(t)
	conn := listenConn(t)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, bumpConfig(tx))
	require.False(t, gotNotify(t, conn, 400*time.Millisecond), "notification delivered before commit")
	require.NoError(t, tx.Commit().Error)
	require.True(t, gotNotify(t, conn, 3*time.Second), "no notification after commit")
}

func TestInstanceConfigTrigger_RollbackEmitsNothing(t *testing.T) {
	db := openNotifyPG(t)
	conn := listenConn(t)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, bumpConfig(tx))
	require.NoError(t, tx.Rollback().Error)
	require.False(t, gotNotify(t, conn, 500*time.Millisecond), "notification delivered on rollback")
}
