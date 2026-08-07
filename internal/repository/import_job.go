package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// staleRunningThreshold bounds how long a job may sit in `running` before the
// startup sweep treats it as orphaned. Kept far longer than any real import
// (which completes in minutes) precisely so a live PEER replica's in-flight job
// is NOT swept when THIS replica starts: under HPA scale-up / rolling deploy a
// peer may be actively processing a freshly-claimed job whose updated_at is only
// seconds old, and flipping it to failed would report a live, succeeding import
// as failed. Residual: an import legitimately running past this threshold could
// still be swept — acceptable for an extreme edge, and the job is re-runnable.
const staleRunningThreshold = time.Hour

// ImportJobRepository persists the async document-import queue (design D7). Rows
// carry the uploaded archive as bytea plus progress counters a worker updates as
// it drains the queue.
type ImportJobRepository struct {
	db *gorm.DB
}

func NewImportJobRepository(db *gorm.DB) *ImportJobRepository {
	return &ImportJobRepository{db: db}
}

// Create inserts a job (typically status=queued) with its archive bytes.
func (r *ImportJobRepository) Create(ctx context.Context, job *models.ImportJob) error {
	if err := r.db.WithContext(ctx).Create(job).Error; err != nil {
		return fmt.Errorf("create import job: %w", err)
	}
	return nil
}

// ClaimNext atomically claims the oldest queued job and flips it to running,
// using SELECT ... FOR UPDATE SKIP LOCKED so multiple worker replicas cooperate
// without ever double-processing a row (design: Risks — multi-replica worker).
// Returns (nil, nil) when the queue holds no claimable job.
func (r *ImportJobRepository) ClaimNext(ctx context.Context) (*models.ImportJob, error) {
	var job models.ImportJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock one queued row, skipping rows a peer replica already holds.
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ?", models.ImportJobStatusQueued).
			Order("created_at ASC").
			First(&job).Error; err != nil {
			return err
		}
		// Flip to running inside the same tx so the claim and the transition are
		// one unit — a peer never sees it as queued again.
		if err := tx.Model(&models.ImportJob{}).
			Where("id = ?", job.ID).
			Update("status", models.ImportJobStatusRunning).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim import job: %w", err)
	}
	job.Status = models.ImportJobStatusRunning
	return &job, nil
}

// UpdateProgress writes the counters of an in-flight job (e.g. seeding total at
// the start of processing) without changing its status. The write is guarded by
// WHERE status = running so it only mutates a job that is still running: if a
// peer replica's startup sweep already reclaimed the row (running->failed), the
// update is a no-op and ErrNotFound is returned, letting the caller tell "job no
// longer running" from success (design D9).
func (r *ImportJobRepository) UpdateProgress(ctx context.Context, id uuid.UUID, total, imported, skipped, failed int) error {
	res := r.db.WithContext(ctx).
		Model(&models.ImportJob{}).
		Where("id = ? AND status = ?", id, models.ImportJobStatusRunning).
		Updates(map[string]any{
			"total":    total,
			"imported": imported,
			"skipped":  skipped,
			"failed":   failed,
		})
	if res.Error != nil {
		return fmt.Errorf("update import job progress: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: import job %s", apperr.ErrNotFound, id)
	}
	return nil
}

// Finish writes the terminal status (succeeded|failed), final counters, and the
// error string (empty on success). The write is guarded by WHERE status = running
// so a terminal row is never overwritten: once a startup sweep marks an orphaned
// job failed, a slower worker's Finish(succeeded) matches no row, is a no-op, and
// returns ErrNotFound. This makes the terminal state deterministic (failed wins),
// matching the interrupted->failed->retry semantics (design D9).
func (r *ImportJobRepository) Finish(ctx context.Context, id uuid.UUID, status, errMsg string, total, imported, skipped, failed int) error {
	res := r.db.WithContext(ctx).
		Model(&models.ImportJob{}).
		Where("id = ? AND status = ?", id, models.ImportJobStatusRunning).
		Updates(map[string]any{
			"status":   status,
			"error":    errMsg,
			"total":    total,
			"imported": imported,
			"skipped":  skipped,
			"failed":   failed,
		})
	if res.Error != nil {
		return fmt.Errorf("finish import job: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: import job %s", apperr.ErrNotFound, id)
	}
	return nil
}

// SweepRunningToFailed reclaims only jobs stuck in `running` past
// staleRunningThreshold (a crashed process left the row orphaned). Called on
// worker start. It deliberately does NOT touch a job a live peer replica is
// actively processing: ClaimNext bumps updated_at at claim time (gorm
// auto-manages UpdatedAt), so a live import's row stays fresh and is skipped by
// the age guard — the operator sees a clean failure to retry only for genuinely
// orphaned rows, not for imports another replica is still running (D9, F3).
func (r *ImportJobRepository) SweepRunningToFailed(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&models.ImportJob{}).
		Where("status = ? AND updated_at < ?", models.ImportJobStatusRunning, time.Now().Add(-staleRunningThreshold)).
		Updates(map[string]any{
			"status": models.ImportJobStatusFailed,
			"error":  "interrupted: server restarted while the job was running",
		})
	if res.Error != nil {
		return 0, fmt.Errorf("sweep running import jobs: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// GetByID returns a job scoped to its owning tenant. ErrNotFound when the id is
// unknown or belongs to a different tenant — a job is visible only to its owner.
func (r *ImportJobRepository) GetByID(ctx context.Context, id, tenantID uuid.UUID) (*models.ImportJob, error) {
	var job models.ImportJob
	if err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: import job %s", apperr.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get import job: %w", err)
	}
	return &job, nil
}

// GetStatusByID returns a job WITHOUT its Archive blob — for the status/poll
// path, which never needs the (up to ~32MiB) archive. ClaimNext/GetByID keep
// loading the full row for the worker. Same tenant scoping and not-found
// mapping as GetByID: a job is visible only to its owning tenant.
func (r *ImportJobRepository) GetStatusByID(ctx context.Context, id, tenantID uuid.UUID) (*models.ImportJob, error) {
	var job models.ImportJob
	if err := r.db.WithContext(ctx).
		Omit("Archive").
		Where("id = ? AND tenant_id = ?", id, tenantID).
		First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: import job %s", apperr.ErrNotFound, id)
		}
		return nil, fmt.Errorf("get import job: %w", err)
	}
	return &job, nil
}
