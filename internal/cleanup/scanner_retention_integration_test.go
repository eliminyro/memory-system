//go:build integration

package cleanup_test

import (
	"context"
	"log/slog"
	"math/rand"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/cleanup"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

const scanTestDim = 768

func openScanPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", scanTestDim, database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// scanGC is a configurable cleanup.GlobalConfig driving the scanner's gates.
type scanGC struct {
	cleanup     bool
	retention   bool
	grace       int
	historyDays int
	metricsDays int
}

func (g scanGC) CleanupEnabled() bool        { return g.cleanup }
func (g scanGC) CleanupIntervalHours() int   { return 24 }
func (g scanGC) HistoryRetentionDays() int   { return g.historyDays }
func (g scanGC) RetentionSweepEnabled() bool { return g.retention }
func (g scanGC) RetentionGraceDays() int     { return g.grace }
func (g scanGC) MetricsRetentionDays() int   { return g.metricsDays }
func (g scanGC) WebhookURL() string          { return "" }

func learningPolicies(expirationDays int) cleanup.PolicySource {
	return staleness.NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		models.DocTypeLearning: {ExpirationAgeDays: expirationDays, Prunable: true},
	})
}

func newScanner(db *gorm.DB, gc cleanup.GlobalConfig, policies cleanup.PolicySource) *cleanup.Scanner {
	return cleanup.NewScanner(
		repository.NewLintRepository(db),
		repository.NewTenantRepository(db),
		repository.NewCleanupQueueRepository(db),
		repository.NewMutationHistoryRepository(db),
		repository.NewRetentionRepository(db),
		repository.NewMetricEventRepository(db),
		policies, gc, nil, slog.Default(),
	)
}

func randVec(rng *rand.Rand) []float32 {
	v := make([]float32, scanTestDim)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return v
}

func seedScanTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, db.Create(&models.Tenant{
		ID: id, Name: "scan-" + uuid.NewString(), StalenessMode: models.StalenessModeOff,
	}).Error)
	return id
}

func seedColdDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, slug string, days int, rng *rand.Rand) uuid.UUID {
	t.Helper()
	doc := &models.Document{
		ID: uuid.New(), TenantID: tenantID, Category: "learnings",
		Slug: slug, Title: slug, DocType: models.DocTypeLearning,
	}
	require.NoError(t, db.Create(doc).Error)
	require.NoError(t, db.Create(&models.Section{
		DocumentID: doc.ID, Ordinal: 0, Content: slug,
		Embedding: pgvector.NewVector(randVec(rng)),
	}).Error)
	require.NoError(t, db.Exec(
		`UPDATE documents SET created_at = NOW() - make_interval(days => ?), last_accessed_at = NULL WHERE id = ?`,
		days, doc.ID).Error)
	require.NoError(t, db.Exec(
		`UPDATE sections SET created_at = NOW() - make_interval(days => ?), verified_at = NULL WHERE document_id = ?`,
		days, doc.ID).Error)
	return doc.ID
}

func docCount(t *testing.T, db *gorm.DB, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&models.Document{}).Where("id = ?", id).Count(&n).Error)
	return n
}

// TestScannerRetention_ToggleGatesEviction covers task 3.1: sweep off deletes
// nothing; sweep on evicts cold docs while fresh docs survive.
func TestScannerRetention_ToggleGatesEviction(t *testing.T) {
	db := openScanPG(t)
	rng := rand.New(rand.NewSource(3))
	tenantID := seedScanTenant(t, db)
	t.Cleanup(func() {
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM deletion_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	})
	ctx := context.Background()

	cold := seedColdDoc(t, db, tenantID, "cold", 150, rng)
	fresh := seedColdDoc(t, db, tenantID, "fresh", 0, rng)
	policies := learningPolicies(100)

	off := newScanner(db, scanGC{}, policies)
	stats, err := off.RunOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.DocsEvicted, "sweep off deletes nothing")
	require.Equal(t, int64(1), docCount(t, db, cold))

	on := newScanner(db, scanGC{retention: true}, policies)
	stats, err = on.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.DocsEvicted)
	require.Zero(t, docCount(t, db, cold), "cold doc evicted when sweep is on")
	require.Equal(t, int64(1), docCount(t, db, fresh), "fresh doc survives")
}

// TestScannerRetention_RetentionOnlyCycle covers task 3.2: a cleanup-off cycle
// still evicts and prunes history, and the bootstrap tenant is skipped.
func TestScannerRetention_RetentionOnlyCycle(t *testing.T) {
	db := openScanPG(t)
	rng := rand.New(rand.NewSource(5))
	tenantID := seedScanTenant(t, db)
	t.Cleanup(func() {
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM deletion_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	})
	ctx := context.Background()

	cold := seedColdDoc(t, db, tenantID, "cold2", 150, rng)
	bootCold := seedColdDoc(t, db, models.BootstrapTenantID, "bootcold", 150, rng)
	t.Cleanup(func() { db.Exec("DELETE FROM documents WHERE id = ?", bootCold) })

	// an old mutation_history row the always-on prune should remove.
	hist := &models.MutationHistory{TenantID: tenantID, DocumentID: cold, OpType: models.MutationOpDeleteDocument}
	require.NoError(t, db.Create(hist).Error)
	require.NoError(t, db.Exec(
		`UPDATE mutation_history SET created_at = NOW() - make_interval(days => 400) WHERE id = ?`, hist.ID).Error)

	sc := newScanner(db, scanGC{cleanup: false, retention: true, historyDays: 90}, learningPolicies(100))
	stats, err := sc.RunOnce(ctx)
	require.NoError(t, err)

	require.Equal(t, 1, stats.DocsEvicted, "retention-only cycle still evicts")
	require.Zero(t, docCount(t, db, cold))
	require.Equal(t, int64(1), docCount(t, db, bootCold), "bootstrap tenant is skipped")
	require.GreaterOrEqual(t, stats.HistoryPruned, 1, "history prune runs whenever the cycle runs")

	var histCount int64
	require.NoError(t, db.Model(&models.MutationHistory{}).Where("id = ?", hist.ID).Count(&histCount).Error)
	require.Zero(t, histCount, "old history row pruned")
}

// TestScannerMetrics_PruneAndCleanupEmit covers tasks 4.2 and 5.2: the cycle prunes
// old metric_events, and a metrics-enabled tenant records one cleanup event per
// evicted doc.
func TestScannerMetrics_PruneAndCleanupEmit(t *testing.T) {
	db := openScanPG(t)
	rng := rand.New(rand.NewSource(9))
	tenantID := uuid.New()
	require.NoError(t, db.Create(&models.Tenant{
		ID: tenantID, Name: "scanm-" + uuid.NewString(),
		StalenessMode: models.StalenessModeOff, MetricsEnabled: true,
	}).Error)
	t.Cleanup(func() {
		db.Exec("DELETE FROM metric_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM deletion_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	})
	ctx := context.Background()

	cold := seedColdDoc(t, db, tenantID, "coldm", 150, rng)

	events := repository.NewMetricEventRepository(db)
	stale := &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventAccess, DocType: models.DocTypeLearning, DocID: &cold}
	require.NoError(t, events.Append(ctx, stale))
	require.NoError(t, db.Exec(`UPDATE metric_events SET created_at = NOW() - make_interval(days => 200) WHERE id = ?`, stale.ID).Error)

	sc := newScanner(db, scanGC{retention: true, metricsDays: 90}, learningPolicies(100))
	stats, err := sc.RunOnce(ctx)
	require.NoError(t, err)

	require.Equal(t, 1, stats.DocsEvicted)
	require.GreaterOrEqual(t, stats.MetricsPruned, 1, "old metric_events pruned whenever the cycle runs")
	require.Zero(t, docCount(t, db, cold))

	var cleanupEvents int64
	require.NoError(t, db.Model(&models.MetricEvent{}).
		Where("tenant_id = ? AND event_type = ?", tenantID, models.MetricEventCleanup).
		Count(&cleanupEvents).Error)
	require.Equal(t, int64(1), cleanupEvents, "one cleanup event per evicted doc for the opted-in tenant")
}
