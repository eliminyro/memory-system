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

func gaugeMap(counts []repository.StalenessCount) map[uuid.UUID]int64 {
	m := map[uuid.UUID]int64{}
	for _, c := range counts {
		m[c.TenantID] += c.Count
	}
	return m
}

// TestMetricEvents_Gauges covers task 6.1's data layer: stale counts regardless of
// mode; expired counts only for hard-mode tenants (matching read-gating).
func TestMetricEvents_Gauges(t *testing.T) {
	db := openLintPG(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(23))
	sections := repository.NewSectionRepository(db)

	offTenant := seedTenant(t, db) // seedTenant defaults staleness off
	t.Cleanup(func() { cleanupTenant(db, offTenant) })
	offDoc := seedRetDoc(t, db, offTenant, "offstale", models.DocTypeLearning, rng)
	coldenDoc(t, db, offDoc, 200)

	hardTenant := seedTenant(t, db)
	require.NoError(t, db.Exec(`UPDATE tenants SET staleness_mode = ? WHERE id = ?`, models.StalenessModeHard, hardTenant).Error)
	t.Cleanup(func() { cleanupTenant(db, hardTenant) })
	hardDoc := seedRetDoc(t, db, hardTenant, "hardexp", models.DocTypeLearning, rng)
	coldenDoc(t, db, hardDoc, 200)
	fresh := seedRetDoc(t, db, hardTenant, "fresh", models.DocTypeLearning, rng)
	require.NoError(t, db.Exec(`UPDATE sections SET verified_at = NOW() WHERE document_id = ?`, fresh).Error)

	verificationDays := map[string]int{models.DocTypeLearning: 30}
	expirationDays := map[string]int{models.DocTypeLearning: 90}

	stale, err := sections.CountStaleByTenant(ctx, verificationDays)
	require.NoError(t, err)
	staleBy := gaugeMap(stale)
	require.Equal(t, int64(1), staleBy[offTenant], "off-mode stale section counted")
	require.Equal(t, int64(1), staleBy[hardTenant], "hard-mode stale section counted")

	expired, err := sections.CountExpiredByTenant(ctx, expirationDays)
	require.NoError(t, err)
	expBy := gaugeMap(expired)
	require.Zero(t, expBy[offTenant], "off-mode tenant is never expired")
	require.Equal(t, int64(1), expBy[hardTenant], "hard-mode expired section counted")
}
