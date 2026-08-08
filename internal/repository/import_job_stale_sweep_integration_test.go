//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestImportJobRepository_SweepOnlyStale proves the F3 age guard: a fresh running
// job (updated_at ~ now — a live peer replica's just-claimed job) is NOT swept,
// while a genuinely orphaned running job (updated_at older than the stale
// threshold) is flipped to failed. Under multi-replica scale-up / rolling deploy a
// newly-started replica's startup sweep must never blanket-fail a peer's in-flight
// import.
func TestImportJobRepository_SweepOnlyStale(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	tenant := uuid.New()

	// A live peer's freshly-claimed job: running with updated_at = now.
	fresh := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusRunning, Archive: []byte("fresh")}
	require.NoError(t, repo.Create(ctx, fresh))
	require.NoError(t, db.Exec(
		"UPDATE import_jobs SET updated_at = ? WHERE id = ?", time.Now(), fresh.ID,
	).Error)

	// A genuinely orphaned job: running with updated_at well past the stale threshold.
	stale := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusRunning, Archive: []byte("stale")}
	require.NoError(t, repo.Create(ctx, stale))
	require.NoError(t, db.Exec(
		"UPDATE import_jobs SET updated_at = ? WHERE id = ?", time.Now().Add(-2*time.Hour), stale.ID,
	).Error)

	// Only the stale row is reclaimed.
	n, err := repo.SweepRunningToFailed(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "only the stale running job may be swept")

	gotFresh, err := repo.GetByID(ctx, fresh.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusRunning, gotFresh.Status, "a live peer's fresh job must stay running")
	require.Empty(t, gotFresh.Error)

	gotStale, err := repo.GetByID(ctx, stale.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusFailed, gotStale.Status, "the orphaned job must be failed")
	require.NotEmpty(t, gotStale.Error)
}
