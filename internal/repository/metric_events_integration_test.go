//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

func cleanupMetricEvents(db *gorm.DB, tenantID uuid.UUID) {
	db.Exec("DELETE FROM metric_events WHERE tenant_id = ?", tenantID)
}

func appendEvent(t *testing.T, db *gorm.DB, tenantID uuid.UUID, evType, docType string, docID *uuid.UUID) {
	t.Helper()
	ev := &models.MetricEvent{TenantID: tenantID, EventType: evType, DocType: docType, DocID: docID}
	require.NoError(t, repository.NewMetricEventRepository(db).Append(context.Background(), ev))
}

// findTop returns the top-accessed row for docID, or nil.
func findTop(rows []repository.TopAccessedDoc, docID uuid.UUID) *repository.TopAccessedDoc {
	for i := range rows {
		if rows[i].DocID == docID {
			return &rows[i]
		}
	}
	return nil
}

// TestMetricEvents_AppendAggregatePrune covers task 4.1: append, windowed counters,
// top-N by access, and the retention prune.
func TestMetricEvents_AppendAggregatePrune(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(21))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupMetricEvents(db, tenantID); cleanupTenant(db, tenantID) })

	repo := repository.NewMetricEventRepository(db)
	realDoc := seedRetDoc(t, db, tenantID, "topdoc", models.DocTypeLearning, rng)
	d1, d2 := uuid.New(), uuid.New()

	for i := 0; i < 5; i++ {
		appendEvent(t, db, tenantID, models.MetricEventAccess, models.DocTypeLearning, &realDoc)
	}
	appendEvent(t, db, tenantID, models.MetricEventAccess, models.DocTypeLearning, &d1)
	appendEvent(t, db, tenantID, models.MetricEventAccess, models.DocTypeLearning, &d1)
	appendEvent(t, db, tenantID, models.MetricEventAccess, models.DocTypeLearning, &d2)
	appendEvent(t, db, tenantID, models.MetricEventVerify, models.DocTypeLearning, &realDoc)
	appendEvent(t, db, tenantID, models.MetricEventCleanup, models.DocTypeTool, &d2)

	since := time.Now().Add(-time.Hour)

	counters, err := repo.CounterCounts(ctx, since)
	require.NoError(t, err)
	got := map[string]int64{}
	for _, c := range counters {
		if c.TenantID == tenantID {
			got[c.DocType+"/"+c.EventType] = c.Count
		}
	}
	require.Equal(t, int64(8), got["learning/access"])
	require.Equal(t, int64(1), got["learning/verify"])
	require.Equal(t, int64(1), got["tool/cleanup"])

	top, err := repo.TopAccessed(ctx, since, 200)
	require.NoError(t, err)
	realTop := findTop(top, realDoc)
	require.NotNil(t, realTop, "real doc appears in top-accessed")
	require.Equal(t, int64(5), realTop.Count)
	require.NotEmpty(t, realTop.Path, "path resolved via LEFT JOIN for a live doc")
	if d1Top := findTop(top, d1); d1Top != nil {
		require.Empty(t, d1Top.Path, "deleted/absent doc has empty path, not a broken join")
	}

	// Prune: backdate the cleanup event and prune older than 30 days.
	require.NoError(t, db.Exec(
		`UPDATE metric_events SET created_at = NOW() - make_interval(days => 40) WHERE tenant_id = ? AND event_type = ?`,
		tenantID, models.MetricEventCleanup).Error)
	pruned, err := repo.PruneOlderThan(ctx, time.Now().AddDate(0, 0, -30))
	require.NoError(t, err)
	require.GreaterOrEqual(t, pruned, int64(1))

	var oldRemaining, accessRemaining int64
	require.NoError(t, db.Model(&models.MetricEvent{}).Where("tenant_id = ? AND event_type = ?", tenantID, models.MetricEventCleanup).Count(&oldRemaining).Error)
	require.Zero(t, oldRemaining, "old cleanup event pruned")
	require.NoError(t, db.Model(&models.MetricEvent{}).Where("tenant_id = ? AND event_type = ?", tenantID, models.MetricEventAccess).Count(&accessRemaining).Error)
	require.Equal(t, int64(8), accessRemaining, "recent access events kept")
}

func gaugeMap(counts []repository.GaugeCount) map[uuid.UUID]int64 {
	m := map[uuid.UUID]int64{}
	for _, c := range counts {
		m[c.TenantID] += c.Count
	}
	return m
}

// TestMetricEvents_Gauges covers the metrics data layer: flagged counts live
// sections carrying the needs-verification flag; archived counts archived docs.
func TestMetricEvents_Gauges(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(23))
	sections := repository.NewSectionRepository(db)

	tenantA := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantA) })
	flaggedDoc := seedRetDoc(t, db, tenantA, "flagged", models.DocTypeLearning, rng)
	require.NoError(t, db.Exec(`UPDATE sections SET flagged_at = NOW(), flag_reason = 'x' WHERE document_id = ?`, flaggedDoc).Error)
	archivedDoc := seedRetDoc(t, db, tenantA, "archived", models.DocTypeLearning, rng)
	require.NoError(t, db.Exec(`UPDATE documents SET archived_at = NOW() WHERE id = ?`, archivedDoc).Error)
	// A clean doc counts in neither gauge.
	seedRetDoc(t, db, tenantA, "clean", models.DocTypeLearning, rng)

	flagged, err := sections.CountFlaggedByTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), gaugeMap(flagged)[tenantA], "one flagged section counted")

	archived, err := sections.CountArchivedByTenant(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), gaugeMap(archived)[tenantA], "one archived document counted")
}
