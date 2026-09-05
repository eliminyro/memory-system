//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

func metricsDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, slug string, ageDays int) uuid.UUID {
	t.Helper()
	doc := &models.Document{ID: uuid.New(), TenantID: tenantID, Category: "learnings", Slug: slug, Title: slug, DocType: models.DocTypeLearning}
	require.NoError(t, db.Create(doc).Error)
	require.NoError(t, db.Create(&models.Section{DocumentID: doc.ID, Ordinal: 0, Content: slug, Embedding: pgvector.NewVector(make([]float32, fakeDim))}).Error)
	if ageDays > 0 {
		require.NoError(t, db.Exec(`UPDATE sections SET created_at = NOW() - make_interval(days => ?), verified_at = NULL WHERE document_id = ?`, ageDays, doc.ID).Error)
	}
	return doc.ID
}

// TestMetricsService_SeriesAndSummary covers tasks 6.1 (Prometheus series + live
// gauges) and 6.2 (dashboard summary with top-accessed from the event log).
func TestMetricsService_SeriesAndSummary(t *testing.T) {
	db := openServicePG(t)
	ctx := context.Background()
	tenantID := uuid.New()
	require.NoError(t, db.Create(&models.Tenant{ID: tenantID, Name: "metrics-" + uuid.NewString(), StalenessMode: models.StalenessModeHard}).Error)
	t.Cleanup(func() {
		db.Exec("DELETE FROM metric_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	})

	staleDoc := metricsDoc(t, db, tenantID, "svc-stale", 200)
	freshDoc := metricsDoc(t, db, tenantID, "svc-fresh", 0)

	events := repository.NewMetricEventRepository(db)
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventAccess, DocType: models.DocTypeLearning, DocID: &staleDoc}))
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventAccess, DocType: models.DocTypeLearning, DocID: &staleDoc}))
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventVerify, DocType: models.DocTypeLearning, DocID: &freshDoc}))

	policies := staleness.NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		models.DocTypeLearning: {VerificationAgeDays: 30, ExpirationAgeDays: 90},
	})
	svc := service.NewMetricsService(events, repository.NewSectionRepository(db), policies)

	series, err := svc.PrometheusSeries(ctx, 24*time.Hour)
	require.NoError(t, err)
	var accessCounter, staleGauge, expiredGauge float64
	for _, s := range series {
		require.NotContains(t, s.Labels, "doc_id", "no per-document label on any series")
		if s.Labels["tenant"] != tenantID.String() || s.Labels["doc_type"] != models.DocTypeLearning {
			continue
		}
		switch {
		case s.Name == service.MetricEventsTotal && s.Labels["event_type"] == models.MetricEventAccess:
			accessCounter = s.Value
		case s.Name == service.MetricStaleSections:
			staleGauge = s.Value
		case s.Name == service.MetricExpiredSections:
			expiredGauge = s.Value
		}
	}
	require.Equal(t, float64(2), accessCounter)
	require.Equal(t, float64(1), staleGauge, "one stale section (age past verification window)")
	require.Equal(t, float64(1), expiredGauge, "one expired section (hard tenant, past expiration window)")

	sum, err := svc.DashboardSummary(ctx, 24*time.Hour, 200)
	require.NoError(t, err)
	require.NotEmpty(t, sum.Counts)
	top := findTopAccessed(sum.TopAccessed, staleDoc)
	require.NotNil(t, top, "top-accessed derived from the event log carries the accessed doc")
	require.Equal(t, int64(2), top.Count)
	require.NotEmpty(t, top.Path, "top-accessed carries per-doc detail (path), allowed outside Prometheus labels")
}

func findTopAccessed(rows []repository.TopAccessedDoc, docID uuid.UUID) *repository.TopAccessedDoc {
	for i := range rows {
		if rows[i].DocID == docID {
			return &rows[i]
		}
	}
	return nil
}
