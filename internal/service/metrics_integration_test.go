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
	require.NoError(t, db.Create(&models.Tenant{ID: tenantID, Name: "metrics-" + uuid.NewString()}).Error)
	t.Cleanup(func() {
		db.Exec("DELETE FROM metric_events WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
		db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
	})

	accessDoc := metricsDoc(t, db, tenantID, "svc-access", 0)
	// A section flagged just now counts in flagged but not soon (grace 30 > 7 days off).
	flaggedDoc := metricsDoc(t, db, tenantID, "svc-flagged", 0)
	require.NoError(t, db.Exec(`UPDATE sections SET flagged_at = NOW(), flag_reason = 'changed' WHERE document_id = ?`, flaggedDoc).Error)
	// A section flagged 24d ago (grace 30) archives in 6 days: counts in flagged + soon.
	soonDoc := metricsDoc(t, db, tenantID, "svc-soon", 0)
	require.NoError(t, db.Exec(`UPDATE sections SET flagged_at = NOW() - make_interval(days => 24), flag_reason = 'changed' WHERE document_id = ?`, soonDoc).Error)
	// An archived document counts in the archived gauge.
	archivedDoc := metricsDoc(t, db, tenantID, "svc-archived", 0)
	require.NoError(t, db.Exec(`UPDATE documents SET archived_at = NOW() WHERE id = ?`, archivedDoc).Error)

	events := repository.NewMetricEventRepository(db)
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventAccess, DocType: models.DocTypeLearning, DocID: &accessDoc}))
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventAccess, DocType: models.DocTypeLearning, DocID: &accessDoc}))
	require.NoError(t, events.Append(ctx, &models.MetricEvent{TenantID: tenantID, EventType: models.MetricEventVerify, DocType: models.DocTypeLearning, DocID: &accessDoc}))

	// learning inherits the reference 30-day grace (non-prunable), driving the soon window.
	policies := staleness.NewPolicyStoreFromEffective(models.DefaultEffectivePolicies)
	svc := service.NewMetricsService(events, repository.NewSectionRepository(db), policies)

	series, err := svc.PrometheusSeries(ctx, 24*time.Hour)
	require.NoError(t, err)
	var accessCounter, flaggedGauge, soonGauge, archivedGauge float64
	for _, s := range series {
		require.NotContains(t, s.Labels, "doc_id", "no per-document label on any series")
		if s.Labels["tenant"] != tenantID.String() || s.Labels["doc_type"] != models.DocTypeLearning {
			continue
		}
		switch {
		case s.Name == service.MetricEventsTotal && s.Labels["event_type"] == models.MetricEventAccess:
			accessCounter = s.Value
		case s.Name == service.MetricFlaggedSections:
			flaggedGauge = s.Value
		case s.Name == service.MetricSoonSections:
			soonGauge = s.Value
		case s.Name == service.MetricArchivedDocuments:
			archivedGauge = s.Value
		}
	}
	require.Equal(t, float64(2), accessCounter)
	require.Equal(t, float64(2), flaggedGauge, "svc-flagged + svc-soon both flagged")
	require.Equal(t, float64(1), soonGauge, "only svc-soon is within 7 days of archiving")
	require.Equal(t, float64(1), archivedGauge, "one archived document")

	sum, err := svc.DashboardSummary(ctx, 24*time.Hour, 200)
	require.NoError(t, err)
	require.NotEmpty(t, sum.Counts)
	top := findTopAccessed(sum.TopAccessed, accessDoc)
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
