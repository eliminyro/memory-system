package service

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/panicguard"
)

// maxDecompressedBytes caps the cumulative decompressed size of all markdown
// entries drained from one archive (zip-bomb guard). The compressed upload is
// already bounded to 32 MiB by the ingest handler; this is a generous 16x of that
// cap, so any legitimate markdown corpus fits while a crafted entry that inflates
// to gigabytes is rejected before it is read into memory.
const maxDecompressedBytes = 16 * 32 << 20 // 512 MiB

// maxImportEntries caps how many markdown docs one archive may create. Bounds a
// DoS where a small compressed zip carries hundreds of thousands of tiny/empty
// entries (the decompressed-byte guard alone never trips on zero-byte entries),
// each of which would otherwise drive a full StoreDocument transaction + authz
// write (and an embedder call for non-empty ones). Well above any real KB import.
const maxImportEntries = 10000

// importJobQueue is the slice of the import_jobs repository the worker drives.
// An interface (not the concrete repo) so the worker's transition logic is
// unit-testable with a fake and no database.
type importJobQueue interface {
	ClaimNext(ctx context.Context) (*models.ImportJob, error)
	UpdateProgress(ctx context.Context, id uuid.UUID, total, imported, skipped, failed int) error
	Finish(ctx context.Context, id uuid.UUID, status, errMsg string, total, imported, skipped, failed int) error
	SweepRunningToFailed(ctx context.Context) (int64, error)
}

// importFunc is the ingest seam — MemoryService.ImportDocuments in production, a
// fake in tests. Keeping it a field lets a status-transition test run without a
// real embedding provider or DB.
type importFunc func(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error)

// ImportWorker drains the import_jobs queue in-process (design D7). It is started
// beside the cleanup scanner in cmd/server/main.go and bound to the root context;
// cancelling that context stops all of its goroutines.
type ImportWorker struct {
	jobs        importJobQueue
	importDocs  importFunc
	concurrency int
	interval    time.Duration
	logger      *slog.Logger
}

// NewImportWorker builds a worker. concurrency < 1 is treated as 1; interval <= 0
// falls back to a sane default poll cadence.
func NewImportWorker(jobs importJobQueue, importDocs importFunc, concurrency int, interval time.Duration, logger *slog.Logger) *ImportWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &ImportWorker{
		jobs:        jobs,
		importDocs:  importDocs,
		concurrency: concurrency,
		interval:    interval,
		logger:      logger,
	}
}

// Start sweeps interrupted jobs (D9) then launches `concurrency` polling
// goroutines. Non-blocking, mirroring cleanup.Scanner.Start.
func (w *ImportWorker) Start(ctx context.Context) {
	if n, err := w.jobs.SweepRunningToFailed(ctx); err != nil {
		w.logger.Error("import worker: sweep interrupted jobs failed", "error", err)
	} else if n > 0 {
		w.logger.Warn("import worker: marked interrupted jobs failed", "count", n)
	}
	for i := 0; i < w.concurrency; i++ {
		go w.loop(ctx)
	}
}

// loop drains the queue, then waits one tick (or ctx cancel) before draining
// again. There is no notify channel, so a ticker is the trigger.
func (w *ImportWorker) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// drain claims and processes jobs until the queue is empty or the context is
// cancelled.
func (w *ImportWorker) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := w.jobs.ClaimNext(ctx)
		if err != nil {
			w.logger.Error("import worker: claim next failed", "error", err)
			return
		}
		if job == nil {
			return // queue empty
		}
		// Recover per job so one panicking import logs + the worker keeps
		// claiming, rather than the whole polling goroutine dying.
		func() {
			defer panicguard.Recover(w.logger, "import job")
			w.process(ctx, job)
		}()
	}
}

// process extracts the job's archive in memory and feeds each .md entry to the
// shared ingest core, driving the row queued->running->succeeded|failed.
//
// Every terminal/progress write is status-guarded (WHERE status = running) in the
// repo. If a peer replica's startup sweep already reclaimed this row as failed
// (rolling deploy, design D9), those writes match no row and report ErrNotFound.
// That is an expected outcome, not an error: the worker logs it and stops so the
// swept "failed" terminal state wins deterministically.
func (w *ImportWorker) process(ctx context.Context, job *models.ImportJob) {
	src, total, err := zipDocSource(job.Archive)
	if err != nil {
		w.finish(ctx, job.ID, models.ImportJobStatusFailed, fmt.Sprintf("open archive: %v", err), 0, 0, 0, 0)
		return
	}

	// Seed the total so a poller sees the expected document count while the job
	// runs; the shared core reports final counts atomically at the end.
	if err := w.jobs.UpdateProgress(ctx, job.ID, total, 0, 0, 0); err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			w.logger.Info("import worker: job no longer running; another worker's startup sweep reclaimed it", "job_id", job.ID)
			return
		}
		w.logger.Warn("import worker: seed progress failed", "job_id", job.ID, "error", err)
	}

	res, err := w.importDocs(ctx, job.TenantID, src)
	if err != nil {
		w.finish(ctx, job.ID, models.ImportJobStatusFailed, err.Error(), total, res.Imported, res.Skipped, res.Failed)
		return
	}

	// A batch where every parseable document failed to store (e.g. embedding
	// backend down) is terminal-Failed, not Succeeded-with-imported=0. A mixed
	// partial success (some imported) still finishes Succeeded.
	if res.Imported == 0 && res.Failed > 0 {
		w.finish(ctx, job.ID, models.ImportJobStatusFailed,
			fmt.Sprintf("all %d document(s) failed to import", res.Failed),
			total, res.Imported, res.Skipped, res.Failed)
		return
	}

	w.finish(ctx, job.ID, models.ImportJobStatusSucceeded, "", total, res.Imported, res.Skipped, res.Failed)
}

// finish writes the terminal row state, treating "job no longer running" as an
// expected outcome rather than an error. When the status-guarded Finish matches
// no row (ErrNotFound) a peer worker's startup sweep already marked the job
// failed, so the terminal state is deterministic (failed wins) — the worker logs
// at info and returns instead of raising an error.
func (w *ImportWorker) finish(ctx context.Context, id uuid.UUID, status, errMsg string, total, imported, skipped, failed int) {
	err := w.jobs.Finish(ctx, id, status, errMsg, total, imported, skipped, failed)
	if err == nil {
		return
	}
	if errors.Is(err, apperr.ErrNotFound) {
		w.logger.Info("import worker: job no longer running; another worker's startup sweep reclaimed it", "job_id", id, "attempted_status", status)
		return
	}
	w.logger.Error("import worker: finish failed", "job_id", id, "attempted_status", status, "error", err)
}

// zipDocSource reads a zip archive from memory and returns a DocSource that emits
// each markdown entry as (name, content), plus the count of such entries. Only
// regular *.md files are emitted; directories, non-markdown entries, and CLAUDE.md
// instruction files are skipped — matching cmd/import's filesystem walk. Extraction
// is fully in-memory (bytes.Reader) so the request->worker handoff needs no
// writable/shared filesystem (design D7).
func zipDocSource(archive []byte) (DocSource, int, error) {
	return zipDocSourceLimited(archive, maxDecompressedBytes)
}

// zipDocSourceLimited is zipDocSource with an injectable decompressed-byte
// ceiling so the zip-bomb guard is unit-testable with a small archive. The
// production entry point passes maxDecompressedBytes.
func zipDocSourceLimited(archive []byte, maxDecompressed int64) (DocSource, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, 0, err
	}

	var entries []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(f.Name, ".md") {
			continue
		}
		if strings.Contains(f.Name, "CLAUDE.md") {
			continue
		}
		entries = append(entries, f)
	}
	if len(entries) > maxImportEntries {
		return nil, 0, fmt.Errorf("archive has %d markdown entries, exceeds the %d limit", len(entries), maxImportEntries)
	}

	src := func(emit func(path string, content []byte) error) error {
		var totalDecompressed int64
		for _, f := range entries {
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("open zip entry %q: %w", f.Name, err)
			}
			// Bound the decompressed read: a tiny compressed entry can inflate to
			// gigabytes (zip bomb). Read at most one byte past the remaining budget
			// so an over-large entry is detected without buffering it whole.
			remaining := maxDecompressed - totalDecompressed
			content, err := io.ReadAll(io.LimitReader(rc, remaining+1))
			_ = rc.Close()
			if err != nil {
				return fmt.Errorf("read zip entry %q: %w", f.Name, err)
			}
			totalDecompressed += int64(len(content))
			if totalDecompressed > maxDecompressed {
				return fmt.Errorf("decompressed archive exceeds %d bytes at entry %q (possible zip bomb)", maxDecompressed, f.Name)
			}
			if len(bytes.TrimSpace(content)) == 0 {
				continue // skip empty entries — they'd create 0-section docs and are a common padding/DoS vector
			}
			if err := emit(f.Name, content); err != nil {
				return err
			}
		}
		return nil
	}
	return src, len(entries), nil
}
