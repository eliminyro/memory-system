//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// errEmbedder is an EmbeddingProvider that always fails, standing in for an
// embedding backend that is down so every StoreDocument call errors.
type errEmbedder struct{ dim int }

func (e errEmbedder) Embed(context.Context, string) (pgvector.Vector, error) {
	return pgvector.Vector{}, errors.New("embedding backend down")
}
func (e errEmbedder) Dimensions() int { return e.dim }

// TestImportWorker_AllFailed_IsTerminalFailed is the M2 regression: a batch where
// every parseable document fails to store (embedding backend down) must finish
// terminal-Failed with imported=0, not Succeeded-with-imported=0.
func TestImportWorker_AllFailed_IsTerminalFailed(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		errEmbedder{dim: fakeDim},
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil,
		store,
	)

	tenant := models.Tenant{ID: uuid.New(), Name: "m2-" + uuid.NewString()}
	require.NoError(t, db.Create(&tenant).Error)

	slug := "gorm-" + uuid.NewString()
	archive := zipBytes(t, map[string]string{
		"learnings/go/" + slug + ".md": "# Title\n\nbody",
	})

	jobs := repository.NewImportJobRepository(db)
	job := &models.ImportJob{TenantID: tenant.ID, Status: models.ImportJobStatusQueued, Archive: archive}
	require.NoError(t, jobs.Create(context.Background(), job))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := service.NewImportWorker(jobs, svc.ImportDocuments, 1, 10*time.Millisecond, nil)
	worker.Start(ctx)

	require.Eventually(t, func() bool {
		got, err := jobs.GetByID(context.Background(), job.ID, tenant.ID)
		return err == nil && got.Status == models.ImportJobStatusFailed
	}, 10*time.Second, 20*time.Millisecond, "a wholly-failed import must reach terminal Failed")

	got, err := jobs.GetByID(context.Background(), job.ID, tenant.ID)
	require.NoError(t, err)
	require.Equal(t, models.ImportJobStatusFailed, got.Status)
	require.Equal(t, 0, got.Imported, "no document stored")
	require.Equal(t, 1, got.Failed, "the one parseable document failed to store")
	require.NotEmpty(t, got.Error)
}
