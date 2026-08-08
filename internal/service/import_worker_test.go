package service

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// buildZip returns an in-memory zip containing the given name->content entries.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestZipDocSource_EmitsMarkdownOnly(t *testing.T) {
	archive := buildZip(t, map[string]string{
		"learnings/go/gorm.md": "gorm body",
		"prefs.md":             "prefs body",
		"sub/nested/deep.md":   "deep body",
		"notes.txt":            "not markdown",
		"CLAUDE.md":            "instructions, must skip",
	})

	src, total, err := zipDocSource(archive)
	if err != nil {
		t.Fatalf("zipDocSource: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 (only .md, excluding CLAUDE.md)", total)
	}

	got := map[string]string{}
	if err := src(func(path string, content []byte) error {
		got[path] = string(content)
		return nil
	}); err != nil {
		t.Fatalf("drain source: %v", err)
	}

	want := map[string]string{
		"learnings/go/gorm.md": "gorm body",
		"prefs.md":             "prefs body",
		"sub/nested/deep.md":   "deep body",
	}
	if len(got) != len(want) {
		t.Fatalf("emitted %d entries, want %d: %v", len(got), len(want), got)
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("entry %q = %q, want %q", p, got[p], c)
		}
	}
}

func TestZipDocSource_InvalidArchive(t *testing.T) {
	if _, _, err := zipDocSource([]byte("definitely not a zip")); err == nil {
		t.Fatal("expected error for non-zip bytes, got nil")
	}
}

// TestZipDocSource_RejectsZipBomb feeds a small archive whose one entry
// decompresses past a low test ceiling and asserts the drain fails with a clear
// zip-bomb error (which the worker then reports as a failed job).
func TestZipDocSource_RejectsZipBomb(t *testing.T) {
	// 4 KiB of highly-compressible content: a few bytes on disk, 4096 decompressed.
	archive := buildZip(t, map[string]string{"big.md": strings.Repeat("A", 4096)})

	// Construction only counts entries; the ceiling is enforced lazily on drain.
	src, total, err := zipDocSourceLimited(archive, 1024) // 1 KiB decompressed ceiling
	if err != nil {
		t.Fatalf("zipDocSourceLimited construct: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}

	err = src(func(path string, content []byte) error { return nil })
	if err == nil {
		t.Fatal("expected drain to fail when a decompressed entry exceeds the ceiling")
	}
	if !strings.Contains(err.Error(), "zip bomb") {
		t.Errorf("error = %q, want it to mention the zip-bomb ceiling", err)
	}
}

// TestZipDocSourceLimited_RejectsTooManyEntries proves the entry-count cap trips
// at construction: a small archive carrying more than maxImportEntries markdown
// entries is rejected before any draining, closing the tiny/empty-entry DoS the
// decompressed-byte guard alone never catches.
func TestZipDocSourceLimited_RejectsTooManyEntries(t *testing.T) {
	files := make(map[string]string, maxImportEntries+1)
	for i := 0; i <= maxImportEntries; i++ { // maxImportEntries+1 entries; empty content keeps the zip tiny
		files[fmt.Sprintf("doc%05d.md", i)] = ""
	}
	archive := buildZip(t, files)

	_, _, err := zipDocSourceLimited(archive, maxDecompressedBytes)
	if err == nil {
		t.Fatal("expected an error rejecting an over-large entry count, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), fmt.Sprintf("%d limit", maxImportEntries)) {
		t.Errorf("error = %q, want it to mention exceeding the %d limit", err, maxImportEntries)
	}
}

// TestZipDocSourceLimited_SkipsEmptyEntries proves empty / whitespace-only .md
// entries are dropped during drain so they never create a 0-section document;
// the returned count stays len(entries) (a harmless progress-seed overcount).
func TestZipDocSourceLimited_SkipsEmptyEntries(t *testing.T) {
	archive := buildZip(t, map[string]string{
		"empty.md":      "",
		"whitespace.md": "  \n\t ",
		"real.md":       "actual content",
	})

	src, total, err := zipDocSourceLimited(archive, maxDecompressedBytes)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 (count is len(entries); empties are a seed overcount)", total)
	}

	var emitted []string
	if err := src(func(path string, content []byte) error {
		emitted = append(emitted, path)
		return nil
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(emitted) != 1 || emitted[0] != "real.md" {
		t.Fatalf("emitted = %v, want only [real.md] (empty/whitespace entries skipped)", emitted)
	}
}

// fakeQueue is a no-DB stand-in for the import_jobs repository, capturing the
// worker's status transitions.
type fakeQueue struct {
	mu         sync.Mutex
	pending    []*models.ImportJob
	sweepCalls int
	sweepN     int64
	progress   []progressCall
	finishes   []finishCall
	claimErr   error
	finishErr  error // returned by Finish after recording the call (e.g. ErrNotFound)
}

type progressCall struct {
	id                               uuid.UUID
	total, imported, skipped, failed int
}

type finishCall struct {
	id                               uuid.UUID
	status, errMsg                   string
	total, imported, skipped, failed int
}

func (q *fakeQueue) ClaimNext(ctx context.Context) (*models.ImportJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.pending) == 0 {
		return nil, nil
	}
	job := q.pending[0]
	q.pending = q.pending[1:]
	job.Status = models.ImportJobStatusRunning
	return job, nil
}

func (q *fakeQueue) UpdateProgress(ctx context.Context, id uuid.UUID, total, imported, skipped, failed int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.progress = append(q.progress, progressCall{id, total, imported, skipped, failed})
	return nil
}

func (q *fakeQueue) Finish(ctx context.Context, id uuid.UUID, status, errMsg string, total, imported, skipped, failed int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.finishes = append(q.finishes, finishCall{id, status, errMsg, total, imported, skipped, failed})
	return q.finishErr
}

func (q *fakeQueue) SweepRunningToFailed(ctx context.Context) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweepCalls++
	return q.sweepN, nil
}

func newTestJob(archive []byte) *models.ImportJob {
	return &models.ImportJob{ID: uuid.New(), TenantID: uuid.New(), Status: models.ImportJobStatusQueued, Archive: archive}
}

func TestImportWorker_ProcessSucceeds(t *testing.T) {
	q := &fakeQueue{}
	// importDocs drains the source so the assertion also proves the zip iterator
	// is wired through to the ingest core.
	imp := func(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error) {
		var res ImportResult
		_ = src(func(path string, content []byte) error {
			res.Imported++
			return nil
		})
		return res, nil
	}
	w := NewImportWorker(q, imp, 1, 0, nil)

	job := newTestJob(buildZip(t, map[string]string{"a.md": "x", "b.md": "y", "note.txt": "skip"}))
	w.process(context.Background(), job)

	if len(q.finishes) != 1 {
		t.Fatalf("Finish called %d times, want 1", len(q.finishes))
	}
	f := q.finishes[0]
	if f.status != models.ImportJobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", f.status)
	}
	if f.total != 2 || f.imported != 2 {
		t.Errorf("total/imported = %d/%d, want 2/2 (only .md emitted)", f.total, f.imported)
	}
	if f.errMsg != "" {
		t.Errorf("errMsg = %q, want empty", f.errMsg)
	}
	// Progress is seeded with the total before ingestion.
	if len(q.progress) != 1 || q.progress[0].total != 2 {
		t.Errorf("progress = %+v, want one call seeding total=2", q.progress)
	}
}

func TestImportWorker_ProcessBadArchiveFails(t *testing.T) {
	q := &fakeQueue{}
	called := false
	imp := func(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error) {
		called = true
		return ImportResult{}, nil
	}
	w := NewImportWorker(q, imp, 1, 0, nil)

	w.process(context.Background(), newTestJob([]byte("corrupt")))

	if called {
		t.Error("ImportDocuments must not run for an unopenable archive")
	}
	if len(q.finishes) != 1 || q.finishes[0].status != models.ImportJobStatusFailed {
		t.Fatalf("finishes = %+v, want one failed", q.finishes)
	}
	if q.finishes[0].errMsg == "" {
		t.Error("failed finish should carry the archive error")
	}
}

func TestImportWorker_ProcessIngestErrorFails(t *testing.T) {
	q := &fakeQueue{}
	imp := func(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error) {
		return ImportResult{Imported: 1, Failed: 2}, errors.New("boom during ingest")
	}
	w := NewImportWorker(q, imp, 1, 0, nil)

	w.process(context.Background(), newTestJob(buildZip(t, map[string]string{"a.md": "x"})))

	if len(q.finishes) != 1 {
		t.Fatalf("Finish called %d times, want 1", len(q.finishes))
	}
	f := q.finishes[0]
	if f.status != models.ImportJobStatusFailed {
		t.Errorf("status = %q, want failed", f.status)
	}
	if f.errMsg != "boom during ingest" {
		t.Errorf("errMsg = %q, want the ingest error", f.errMsg)
	}
}

// TestImportWorker_FinishNotRunningIsNotError proves that when Finish reports the
// job is no longer running (ErrNotFound — a peer worker's startup sweep already
// reclaimed it as failed), the worker treats it as an expected outcome: it logs
// at info, NOT error, so the swept "failed" terminal state wins deterministically.
func TestImportWorker_FinishNotRunningIsNotError(t *testing.T) {
	q := &fakeQueue{finishErr: fmt.Errorf("%w: import job gone", apperr.ErrNotFound)}
	imp := func(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error) {
		return ImportResult{Imported: 1}, nil
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w := NewImportWorker(q, imp, 1, 0, logger)

	w.process(context.Background(), newTestJob(buildZip(t, map[string]string{"a.md": "x"})))

	if len(q.finishes) != 1 || q.finishes[0].status != models.ImportJobStatusSucceeded {
		t.Fatalf("finishes = %+v, want one attempted succeeded", q.finishes)
	}
	logs := buf.String()
	if strings.Contains(logs, "level=ERROR") {
		t.Errorf("a not-running Finish must not log at error level; got:\n%s", logs)
	}
	if !strings.Contains(logs, "no longer running") {
		t.Errorf("expected an info log noting the job is no longer running; got:\n%s", logs)
	}
}

func TestImportWorker_StartSweepsInterrupted(t *testing.T) {
	q := &fakeQueue{sweepN: 3}
	w := NewImportWorker(q, func(context.Context, uuid.UUID, DocSource) (ImportResult, error) {
		return ImportResult{}, nil
	}, 2, 0, nil)

	// Cancelled context: Start still sweeps synchronously, and the polling
	// goroutines exit immediately without processing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Start(ctx)

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sweepCalls != 1 {
		t.Fatalf("SweepRunningToFailed called %d times, want 1 on start", q.sweepCalls)
	}
}
