//go:build integration

package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

func edgeEmb() []float32 {
	e := make([]float32, dupTestDim)
	e[0] = 1
	return e
}

// TestEdge_CreateIdempotentOnConflict: a duplicate triple returns the existing
// edge with created=false and leaves exactly one row.
func TestEdge_CreateIdempotentOnConflict(t *testing.T) {
	db := openLintPG(t)
	repo := repository.NewEdgeRepository(db)
	ctx := context.Background()

	ten := seedTenant(t, db)
	src := seedDoc(t, db, ten, "src-"+uuid.NewString(), edgeEmb())
	tgt := seedDoc(t, db, ten, "tgt-"+uuid.NewString(), edgeEmb())

	got1, created1, err := repo.Create(ctx, &models.Edge{TenantID: ten, SourceDocumentID: src, TargetDocumentID: tgt, EdgeType: models.EdgeRelatesTo})
	require.NoError(t, err)
	require.True(t, created1, "first create inserts a new edge")

	got2, created2, err := repo.Create(ctx, &models.Edge{TenantID: ten, SourceDocumentID: src, TargetDocumentID: tgt, EdgeType: models.EdgeRelatesTo})
	require.NoError(t, err)
	require.False(t, created2, "duplicate triple returns the existing edge")
	require.Equal(t, got1.ID, got2.ID)

	var n int64
	require.NoError(t, db.Model(&models.Edge{}).
		Where("source_document_id = ? AND target_document_id = ? AND edge_type = ?", src, tgt, models.EdgeRelatesTo).
		Count(&n).Error)
	require.EqualValues(t, 1, n, "exactly one row for the triple")
}

// TestEdge_ListIncludesArchivedEndpoint: listing must NOT filter the far endpoint
// on archived_at, so a supersede trail to an archived target stays visible.
func TestEdge_ListIncludesArchivedEndpoint(t *testing.T) {
	db := openLintPG(t)
	edges := repository.NewEdgeRepository(db)
	docs := repository.NewDocumentRepository(db)
	ctx := context.Background()

	ten := seedTenant(t, db)
	src := seedDoc(t, db, ten, "src-"+uuid.NewString(), edgeEmb())
	tgt := seedDoc(t, db, ten, "tgt-"+uuid.NewString(), edgeEmb())

	n, err := docs.ArchiveByID(ctx, tgt, models.ArchiveReasonSuperseded)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	_, _, err = edges.Create(ctx, &models.Edge{TenantID: ten, SourceDocumentID: src, TargetDocumentID: tgt, EdgeType: models.EdgeSupersedes})
	require.NoError(t, err)

	scope := []uuid.UUID{ten}
	out, err := edges.ListByDocument(ctx, src, scope)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "outgoing", out[0].Direction)
	require.Equal(t, tgt, out[0].OtherDocumentID)
	require.True(t, out[0].OtherDocumentArchived, "archived endpoint must still be listed")

	in, err := edges.ListByDocument(ctx, tgt, scope)
	require.NoError(t, err)
	require.Len(t, in, 1)
	require.Equal(t, "incoming", in[0].Direction)
	require.Equal(t, src, in[0].OtherDocumentID)
}

// TestEdge_AtomicRollback: the tx-bound edge insert + target archive both roll
// back on a forced error, so a failed supersede leaves the target un-archived.
func TestEdge_AtomicRollback(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()

	ten := seedTenant(t, db)
	src := seedDoc(t, db, ten, "src-"+uuid.NewString(), edgeEmb())
	tgt := seedDoc(t, db, ten, "tgt-"+uuid.NewString(), edgeEmb())

	forced := errors.New("forced failure")
	err := db.Transaction(func(tx *gorm.DB) error {
		txEdges := repository.NewEdgeRepository(tx)
		txDocs := repository.NewDocumentRepository(tx)
		if _, _, e := txEdges.Create(ctx, &models.Edge{TenantID: ten, SourceDocumentID: src, TargetDocumentID: tgt, EdgeType: models.EdgeSupersedes}); e != nil {
			return e
		}
		if _, e := txDocs.ArchiveByID(ctx, tgt, models.ArchiveReasonSuperseded); e != nil {
			return e
		}
		return forced
	})
	require.ErrorIs(t, err, forced)

	var edgeCount int64
	require.NoError(t, db.Model(&models.Edge{}).Where("source_document_id = ?", src).Count(&edgeCount).Error)
	require.EqualValues(t, 0, edgeCount, "rolled-back edge must not persist")

	var d models.Document
	require.NoError(t, db.First(&d, tgt).Error)
	require.Nil(t, d.ArchivedAt, "rolled-back archive must leave the target active")
}

// TestEdge_CascadeOnHardDelete: hard-deleting an endpoint removes its edges via
// the OnDelete:CASCADE FK.
func TestEdge_CascadeOnHardDelete(t *testing.T) {
	db := openLintPG(t)
	edges := repository.NewEdgeRepository(db)
	ctx := context.Background()

	ten := seedTenant(t, db)
	src := seedDoc(t, db, ten, "src-"+uuid.NewString(), edgeEmb())
	tgt := seedDoc(t, db, ten, "tgt-"+uuid.NewString(), edgeEmb())

	_, _, err := edges.Create(ctx, &models.Edge{TenantID: ten, SourceDocumentID: src, TargetDocumentID: tgt, EdgeType: models.EdgeRelatesTo})
	require.NoError(t, err)

	require.NoError(t, db.Delete(&models.Document{}, src).Error)

	var n int64
	require.NoError(t, db.Model(&models.Edge{}).
		Where("source_document_id = ? OR target_document_id = ?", src, tgt).Count(&n).Error)
	require.EqualValues(t, 0, n, "edges cascade-deleted with their endpoint")
}

// TestEdge_ArchiveByIDIdempotent: the AND archived_at IS NULL guard makes a
// repeat archive a 0-row no-op, leaving the reason intact.
func TestEdge_ArchiveByIDIdempotent(t *testing.T) {
	db := openLintPG(t)
	docs := repository.NewDocumentRepository(db)
	ctx := context.Background()

	ten := seedTenant(t, db)
	tgt := seedDoc(t, db, ten, "tgt-"+uuid.NewString(), edgeEmb())

	n1, err := docs.ArchiveByID(ctx, tgt, models.ArchiveReasonSuperseded)
	require.NoError(t, err)
	require.EqualValues(t, 1, n1)

	n2, err := docs.ArchiveByID(ctx, tgt, models.ArchiveReasonSuperseded)
	require.NoError(t, err)
	require.EqualValues(t, 0, n2, "already-archived target is a no-op")

	var d models.Document
	require.NoError(t, db.First(&d, tgt).Error)
	require.NotNil(t, d.ArchivedAt)
	require.Equal(t, models.ArchiveReasonSuperseded, d.ArchiveReason)
}

// TestEdge_MigrationIdempotent: re-running the migration is a no-op and the
// unique triple index exists exactly once.
func TestEdge_MigrationIdempotent(t *testing.T) {
	db := openLintPG(t)

	require.NoError(t, database.Migrate(db, "fake", "fake", dupTestDim, database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()))

	var idx int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_document_edges_triple'`).Scan(&idx).Error)
	require.EqualValues(t, 1, idx, "unique triple index exists exactly once")
}
