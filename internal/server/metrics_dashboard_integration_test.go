//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// metricsSummaryResp mirrors the JSON of service.DashboardSummary for decoding.
type metricsSummaryResp struct {
	WindowDays int `json:"window_days"`
	Counts     []struct {
		TenantID  uuid.UUID `json:"tenant_id"`
		DocType   string    `json:"doc_type"`
		EventType string    `json:"event_type"`
		Count     int64     `json:"count"`
	} `json:"counts"`
	TopAccessed []struct {
		DocID uuid.UUID `json:"doc_id"`
		Count int64     `json:"count"`
	} `json:"top_accessed"`
}

func newMetricsHandler(t *testing.T) (*apiHandler, context.Context, context.Context, *repository.MetricEventRepository) {
	t.Helper()
	db := openAPIPG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(apiTestDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil,
		nil, nil,
		store,
	)
	events := repository.NewMetricEventRepository(db)
	ps := staleness.NewPolicyStore(db)
	require.NoError(t, ps.Load(context.Background()))
	h := &apiHandler{memory: svc, metrics: service.NewMetricsService(events, repository.NewSectionRepository(db), ps)}
	return h, auth.WithLocalAdmin(context.Background()), userCtx(uuid.New(), "nobody-"+uuid.NewString()), events
}

// TestAdminMetrics_NonAdminRefused proves GET /admin/metrics is admin-only.
func TestAdminMetrics_NonAdminRefused(t *testing.T) {
	h, _, nonAdmin, _ := newMetricsHandler(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, "/admin/metrics", nonAdmin, nil))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestAdminMetrics_AdminSummary proves an admin receives the summary, ?days sets
// the window, and a recorded access event surfaces in counts + top-accessed.
func TestAdminMetrics_AdminSummary(t *testing.T) {
	h, admin, _, events := newMetricsHandler(t)

	tid := uuid.New()
	docID := uuid.New()
	require.NoError(t, events.Append(context.Background(), &models.MetricEvent{
		TenantID: tid, EventType: models.MetricEventAccess, DocID: &docID, DocType: "learning",
	}))

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, "/admin/metrics?days=7&top=5", admin, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp metricsSummaryResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 7, resp.WindowDays)

	var inCounts bool
	for _, c := range resp.Counts {
		if c.TenantID == tid && c.DocType == "learning" && c.EventType == models.MetricEventAccess {
			require.Equal(t, int64(1), c.Count)
			inCounts = true
		}
	}
	require.True(t, inCounts, "the recorded access event must appear in counts")

	var inTop bool
	for _, d := range resp.TopAccessed {
		if d.DocID == docID {
			inTop = true
		}
	}
	require.True(t, inTop, "the accessed doc must appear in top_accessed")
}
