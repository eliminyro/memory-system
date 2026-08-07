//go:build integration

package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// zipBytes builds an in-memory zip from name->content entries.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// TestImportWorker_EndToEnd drives a queued job through the real worker + shared
// ingest core against Postgres: queued -> running -> succeeded, with documents
// landing under the owning tenant (spec: Background worker).
func TestImportWorker_EndToEnd(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	adminSubj := "admin-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(adminSubj)))
	adminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: adminSubj})

	target, err := svc.CreateTenant(adminCtx, "worker-target-"+uuid.NewString(), "")
	require.NoError(t, err)

	slug := "gorm-" + uuid.NewString()
	archive := zipBytes(t, map[string]string{
		"learnings/go/" + slug + ".md": "# Title\n\nbody",
		"CLAUDE.md":                    "instructions, skipped",
		"notes.txt":                    "not markdown, skipped",
	})

	jobs := repository.NewImportJobRepository(db)
	job := &models.ImportJob{TenantID: target.ID, Status: models.ImportJobStatusQueued, Archive: archive}
	require.NoError(t, jobs.Create(context.Background(), job))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := service.NewImportWorker(jobs, svc.ImportDocuments, 1, 10*time.Millisecond, nil)
	worker.Start(ctx)

	require.Eventually(t, func() bool {
		got, err := jobs.GetByID(context.Background(), job.ID, target.ID)
		return err == nil && got.Status == models.ImportJobStatusSucceeded
	}, 10*time.Second, 20*time.Millisecond, "worker should drive the job to succeeded")

	got, err := jobs.GetByID(context.Background(), job.ID, target.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.Total, "only the one .md entry counts")
	require.Equal(t, 1, got.Imported)

	docs := repository.NewDocumentRepository(db)
	subcat := "go"
	doc, err := docs.GetByPath(context.Background(), repository.ReadTenants(target.ID), target.ID, "learnings", &subcat, slug)
	require.NoError(t, err, "imported document must be stored under the target tenant")
	require.Equal(t, target.ID, doc.TenantID)
}

// TestImportWorker_SweepMarksInterrupted verifies a job left running (a prior
// process died) is swept to failed on worker start (design D9).
func TestImportWorker_SweepMarksInterrupted(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	tenant := uuid.New()
	jobs := repository.NewImportJobRepository(db)
	stuck := &models.ImportJob{TenantID: tenant, Status: models.ImportJobStatusRunning, Archive: []byte("x")}
	require.NoError(t, jobs.Create(context.Background(), stuck))
	// Backdate past the stale threshold so the startup sweep treats it as a
	// genuinely orphaned job (F3: the sweep no longer touches a fresh/live
	// running row a peer replica may still be processing).
	require.NoError(t, db.Exec("UPDATE import_jobs SET updated_at = ? WHERE id = ?",
		time.Now().Add(-2*time.Hour), stuck.ID).Error)

	// Live context: Start runs SweepRunningToFailed(ctx) synchronously, so a
	// cancelled ctx would abort the sweep's DB write. The poll goroutines can't
	// interfere — the swept job is now failed, not queued, so ClaimNext skips it —
	// and they stop when defer cancel fires.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := service.NewImportWorker(jobs, svc.ImportDocuments, 1, time.Second, nil)
	worker.Start(ctx)

	got, err := jobs.GetByID(context.Background(), stuck.ID, tenant)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusFailed, got.Status)
	require.NotEmpty(t, got.Error)
}
