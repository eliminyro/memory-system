//go:build integration

package database_test

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

const swapDim = 768

func openSwapPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// seedSwapVector populates the corpus with a single embedding so the guard
// enforces (an empty corpus always adopts). Returns a cleanup func.
func seedSwapVector(t *testing.T, db *gorm.DB) func() {
	t.Helper()
	tn := &models.Tenant{ID: uuid.New(), Name: "swap-" + uuid.NewString(), StalenessMode: models.StalenessModeOff}
	require.NoError(t, db.Create(tn).Error)
	doc := &models.Document{
		ID:       uuid.New(),
		TenantID: tn.ID,
		Category: "learnings",
		Slug:     "swap-" + uuid.NewString(),
		Title:    "swap",
		DocType:  "learning",
	}
	require.NoError(t, db.Create(doc).Error)
	emb := make([]float32, swapDim)
	emb[0] = 1
	sec := &models.Section{DocumentID: doc.ID, Ordinal: 0, Content: "swap", Embedding: pgvector.NewVector(emb)}
	require.NoError(t, db.Create(sec).Error)
	return func() {
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tn.ID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tn.ID)
	}
}

// TestMigrateEmbeddingIdentityGuard exercises audit #13/#16: migration records
// the embedding identity for an empty corpus, accepts an unchanged identity, and
// refuses a provider/model swap on a populated corpus (even at the same dim),
// while the dimension guard still refuses a dim change.
func TestMigrateEmbeddingIdentityGuard(t *testing.T) {
	db := openSwapPG(t)
	td := database.TenantColumnDefaults{StalenessMode: "off"}
	gc := database.BaselineGlobalConfigDefaults()

	// Ensure the schema exists before we manipulate the metadata row.
	require.NoError(t, database.Migrate(db, "fake", "fake", swapDim, td, gc))

	// Clean slate: no metadata row so the next migrate adopts. Remove the row on
	// teardown so packages sharing this DB re-adopt their own identity.
	require.NoError(t, db.Exec("DELETE FROM embedding_metadata").Error)
	t.Cleanup(func() { db.Exec("DELETE FROM embedding_metadata") })

	// 1. Empty / no metadata -> adopt.
	require.NoError(t, database.Migrate(db, "fake", "model-a", swapDim, td, gc))
	var meta models.EmbeddingMetadata
	require.NoError(t, db.First(&meta).Error)
	require.Equal(t, "fake", meta.Provider)
	require.Equal(t, "model-a", meta.Model)
	require.Equal(t, swapDim, meta.Dimensions)

	// 2. Same provider/model -> ok (idempotent).
	require.NoError(t, database.Migrate(db, "fake", "model-a", swapDim, td, gc))

	// Populate the corpus so the identity is now frozen.
	cleanupVec := seedSwapVector(t, db)
	t.Cleanup(cleanupVec)

	// 3. Changed model at the same dimension -> refuse.
	err := database.Migrate(db, "fake", "model-b", swapDim, td, gc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "model-a")
	require.Contains(t, err.Error(), "model-b")
	require.Contains(t, err.Error(), "re-embed")

	// 3b. Changed provider at the same dimension -> refuse.
	err = database.Migrate(db, "gcp", "model-a", swapDim, td, gc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider")

	// 4. Changed dimension -> refuse (existing dimension guard preserved).
	err = database.Migrate(db, "fake", "model-a", 1536, td, gc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dimension mismatch")

	// Refusals must not have mutated the recorded identity.
	var after models.EmbeddingMetadata
	require.NoError(t, db.First(&after).Error)
	require.Equal(t, "fake", after.Provider)
	require.Equal(t, "model-a", after.Model)
	require.Equal(t, swapDim, after.Dimensions)
}
