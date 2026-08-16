//go:build usagebench

// TestUsageBench is the runnable entry for the usage-weighted-ranking benchmark.
// It is gated behind the `usagebench` build tag so it never runs in the normal
// `go test` path. CI invokes it explicitly:
//
//	go test -tags usagebench -run TestUsageBench -timeout 30m ./benchmarks/usagebench/
//
// It needs a local Postgres+pgvector via TEST_DATABASE_URL (same as the
// integration suite) and never touches prod or the `pe` tenant. It writes
// results.json + RESULTS.md into the package directory (override with
// USAGEBENCH_OUT).
package usagebench

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/database"
)

func TestUsageBench(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; usagebench requires local Postgres")
	}

	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, "fake", "fake", 768, database.TenantColumnDefaults{StalenessMode: "off"}))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	store := authz.NewPostgresStore(db)
	cfg := DefaultRunConfig()

	res, err := Run(context.Background(), db, store, cfg)
	require.NoError(t, err)

	outDir := os.Getenv("USAGEBENCH_OUT")
	if outDir == "" {
		outDir = "."
	}
	require.NoError(t, WriteJSON(filepath.Join(outDir, "results.json"), res))
	require.NoError(t, WriteMarkdown(filepath.Join(outDir, "RESULTS.md"), res))
	t.Logf("usagebench verdict: %s (%s)", res.Verdict.Decision, res.Verdict.Rationale)
}
