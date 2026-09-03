//go:build integration

package repository_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// openImportJobPG connects the repository integration suite to TEST_DATABASE_URL,
// migrating with the shared 768-dim so a co-located test DB stays consistent.
func openImportJobPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, "fake", "fake", 768, database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestImportJobRepository_ClaimAndSweep(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	tenant := uuid.New()

	j1 := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusQueued, Archive: []byte("a")}
	j2 := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusQueued, Archive: []byte("b")}
	require.NoError(t, repo.Create(ctx, j1))
	require.NoError(t, repo.Create(ctx, j2))

	// ClaimNext returns queued jobs oldest-first, flipping each to running.
	c1, err := repo.ClaimNext(ctx)
	require.NoError(t, err)
	require.NotNil(t, c1)
	require.Equal(t, models.ImportJobStatusRunning, c1.Status)
	require.Equal(t, j1.ID, c1.ID)
	require.Equal(t, []byte("a"), c1.Archive, "claim must load the archive bytes for the worker")

	c2, err := repo.ClaimNext(ctx)
	require.NoError(t, err)
	require.NotNil(t, c2)
	require.Equal(t, j2.ID, c2.ID)

	// Queue drained: no claimable job.
	c3, err := repo.ClaimNext(ctx)
	require.NoError(t, err)
	require.Nil(t, c3)

	// Backdate both running rows past the stale threshold so the sweep — which now
	// reclaims only genuinely orphaned jobs (F3), never a live peer's fresh claim —
	// treats them as interrupted.
	require.NoError(t, db.Exec(
		"UPDATE import_jobs SET updated_at = ? WHERE id IN (?, ?)",
		time.Now().Add(-2*time.Hour), j1.ID, j2.ID,
	).Error)

	// SweepRunningToFailed flips both stale running jobs to failed (interrupted).
	n, err := repo.SweepRunningToFailed(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	got, err := repo.GetByID(ctx, j1.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusFailed, got.Status)
	require.NotEmpty(t, got.Error)
}

func TestImportJobRepository_ProgressAndFinish(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	tenant := uuid.New()

	job := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusQueued, Archive: []byte("z")}
	require.NoError(t, repo.Create(ctx, job))

	// Progress + finish are status-guarded (WHERE status = running), so the row
	// must first be claimed queued->running before either write applies.
	claimed, err := repo.ClaimNext(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, job.ID, claimed.ID)

	require.NoError(t, repo.UpdateProgress(ctx, job.ID, 5, 0, 0, 0))
	require.NoError(t, repo.Finish(ctx, job.ID, models.ImportJobStatusSucceeded, "", 5, 4, 1, 0))

	got, err := repo.GetByID(ctx, job.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusSucceeded, got.Status)
	require.Equal(t, 5, got.Total)
	require.Equal(t, 4, got.Imported)
	require.Equal(t, 1, got.Skipped)
	require.Empty(t, got.Error)
}

// TestImportJobRepository_TerminalNotOverwritten proves the status guard on
// Finish/UpdateProgress: once a startup sweep reclaims a running row as failed, a
// slower worker's terminal writes match no row (ErrNotFound) and leave the failed
// state untouched — failed wins deterministically (design D9).
func TestImportJobRepository_TerminalNotOverwritten(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	tenant := uuid.New()

	job := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusQueued, Archive: []byte("z")}
	require.NoError(t, repo.Create(ctx, job))

	claimed, err := repo.ClaimNext(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, job.ID, claimed.ID)

	// Backdate the running row past the stale threshold so the startup sweep (F3)
	// reclaims it — simulating a genuinely orphaned job from a crashed process.
	require.NoError(t, db.Exec(
		"UPDATE import_jobs SET updated_at = ? WHERE id = ?",
		time.Now().Add(-2*time.Hour), job.ID,
	).Error)

	// A peer replica's startup sweep reclaims the stale running row as failed.
	n, err := repo.SweepRunningToFailed(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// The slower worker's Finish(succeeded) must be a no-op: the row is no longer
	// running, so it matches nothing and returns ErrNotFound.
	err = repo.Finish(ctx, job.ID, models.ImportJobStatusSucceeded, "", 3, 3, 0, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, apperr.ErrNotFound))

	// A late UpdateProgress is likewise rejected.
	err = repo.UpdateProgress(ctx, job.ID, 9, 9, 0, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, apperr.ErrNotFound))

	// The failed terminal state is preserved untouched — the losing Finish's
	// counters and status were never written.
	got, err := repo.GetByID(ctx, job.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusFailed, got.Status, "failed must win over a slower Finish(succeeded)")
	require.NotEmpty(t, got.Error, "the interrupted sweep message must survive")
	require.Equal(t, 0, got.Imported, "counters from the losing writes must not be applied")
}

// TestImportJobRepository_ConcurrentClaimNoDoubleClaim spawns several concurrent
// ClaimNext callers against a batch of queued rows and asserts no job id is
// claimed twice — the FOR UPDATE SKIP LOCKED no-double-claim guarantee.
func TestImportJobRepository_ConcurrentClaimNoDoubleClaim(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	tenant := uuid.New()

	const jobs = 12
	ids := make(map[uuid.UUID]bool, jobs)
	for i := 0; i < jobs; i++ {
		j := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusQueued, Archive: []byte("x")}
		require.NoError(t, repo.Create(ctx, j))
		ids[j.ID] = true
	}

	const workers = 4
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		claimed   = map[uuid.UUID]int{}
		claimErrs []error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, err := repo.ClaimNext(ctx)
				if err != nil {
					mu.Lock()
					claimErrs = append(claimErrs, err)
					mu.Unlock()
					return
				}
				if job == nil {
					return // queue drained
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Empty(t, claimErrs, "concurrent ClaimNext must not error")
	require.Len(t, claimed, jobs, "every queued row claimed exactly once")
	for id, c := range claimed {
		require.Equal(t, 1, c, "job %s claimed %d times; SKIP LOCKED must prevent double-claim", id, c)
		require.True(t, ids[id], "claimed an id that was never queued: %s", id)
	}
}

func TestImportJobRepository_GetByIDTenantScoped(t *testing.T) {
	db := openImportJobPG(t)
	repo := repository.NewImportJobRepository(db)
	ctx := context.Background()
	owner := uuid.New()
	other := uuid.New()

	job := &models.ImportJob{TenantID: owner, Status: models.ImportJobStatusQueued, Archive: []byte("x")}
	require.NoError(t, repo.Create(ctx, job))

	// Owner sees it.
	_, err := repo.GetByID(ctx, job.ID, owner)
	require.NoError(t, err)

	// A different tenant does not — ErrNotFound, not a leak.
	_, err = repo.GetByID(ctx, job.ID, other)
	require.Error(t, err)
	require.True(t, errors.Is(err, apperr.ErrNotFound))

	// Unknown id is also ErrNotFound.
	_, err = repo.GetByID(ctx, uuid.New(), owner)
	require.True(t, errors.Is(err, apperr.ErrNotFound))
}
