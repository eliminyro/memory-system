package service

import (
	"context"
	"time"

	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// MetricsService turns the metric_events log and live corpus state into bounded,
// Prometheus-shaped aggregates for the admin dashboard and a later /metrics
// endpoint. Read-only — it records nothing.
type MetricsService struct {
	events   *repository.MetricEventRepository
	sections *repository.SectionRepository
	policies *staleness.PolicyStore
}

func NewMetricsService(events *repository.MetricEventRepository, sections *repository.SectionRepository, policies *staleness.PolicyStore) *MetricsService {
	return &MetricsService{events: events, sections: sections, policies: policies}
}

// Prometheus metric names + series types for the exposed series.
const (
	MetricEventsTotal       = "memory_events_total"
	MetricFlaggedSections   = "memory_flagged_sections"
	MetricSoonSections      = "memory_soon_sections"
	MetricArchivedDocuments = "memory_archived_documents"

	seriesTypeCounter = "counter"
	seriesTypeGauge   = "gauge"
)

// Series is one Prometheus-compatible sample: a metric name, its type, bounded
// labels (tenant, doc_type, and event_type for counters — never a per-doc id), and
// the value. The same shape feeds the WebUI summary now and /metrics later.
type Series struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// PrometheusSeries returns the counters (from the event log over window) plus the
// live flagged/archived gauges, with label cardinality bounded to tenant × doc_type
// (× event_type for counters).
func (m *MetricsService) PrometheusSeries(ctx context.Context, window time.Duration) ([]Series, error) {
	since := time.Now().Add(-window)
	counters, err := m.events.CounterCounts(ctx, since)
	if err != nil {
		return nil, err
	}
	flagged, soon, archived, err := m.gauges(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(counters)+len(flagged)+len(soon)+len(archived))
	for _, c := range counters {
		out = append(out, Series{
			Name: MetricEventsTotal, Type: seriesTypeCounter,
			Labels: map[string]string{"tenant": c.TenantID.String(), "doc_type": c.DocType, "event_type": c.EventType},
			Value:  float64(c.Count),
		})
	}
	out = append(out, gaugeSeries(MetricFlaggedSections, flagged)...)
	out = append(out, gaugeSeries(MetricSoonSections, soon)...)
	out = append(out, gaugeSeries(MetricArchivedDocuments, archived)...)
	return out, nil
}

func gaugeSeries(name string, counts []repository.GaugeCount) []Series {
	out := make([]Series, 0, len(counts))
	for _, g := range counts {
		out = append(out, Series{
			Name: name, Type: seriesTypeGauge,
			Labels: map[string]string{"tenant": g.TenantID.String(), "doc_type": g.DocType},
			Value:  float64(g.Count),
		})
	}
	return out
}

// gauges computes the live gauge counts (tenant × doc_type): flagged = sections
// carrying the needs-verification flag; soon = flagged sections within
// soonWindowDays of their archive point; archived = documents past grace, archived.
func (m *MetricsService) gauges(ctx context.Context) (flagged, soon, archived []repository.GaugeCount, err error) {
	if flagged, err = m.sections.CountFlaggedByTenant(ctx); err != nil {
		return nil, nil, nil, err
	}
	var cutoffs map[string]int
	if m.policies != nil {
		cutoffs = repository.BuildArchiveCutoffs(m.policies.All())
	}
	if soon, err = m.sections.CountSoonByTenant(ctx, cutoffs); err != nil {
		return nil, nil, nil, err
	}
	if archived, err = m.sections.CountArchivedByTenant(ctx); err != nil {
		return nil, nil, nil, err
	}
	return flagged, soon, archived, nil
}

// DashboardSummary is the admin dashboard payload: event counts over a window, the
// live flagged/archived gauges, and the top-accessed documents (per-doc detail from
// the event log, allowed here since it is not a Prometheus label).
type DashboardSummary struct {
	WindowDays    int                         `json:"window_days"`
	Since         time.Time                   `json:"since"`
	Counts        []repository.MetricCounter  `json:"counts"`
	FlaggedGauge  []repository.GaugeCount     `json:"flagged_sections"`
	SoonGauge     []repository.GaugeCount     `json:"soon_sections"`
	ArchivedGauge []repository.GaugeCount     `json:"archived_documents"`
	TopAccessed   []repository.TopAccessedDoc `json:"top_accessed"`
}

// DashboardSummary assembles the admin summary over window, listing the topN
// most-accessed documents. Phase D's GET /api/admin/metrics calls this.
func (m *MetricsService) DashboardSummary(ctx context.Context, window time.Duration, topN int) (*DashboardSummary, error) {
	since := time.Now().Add(-window)
	counts, err := m.events.CounterCounts(ctx, since)
	if err != nil {
		return nil, err
	}
	flagged, soon, archived, err := m.gauges(ctx)
	if err != nil {
		return nil, err
	}
	top, err := m.events.TopAccessed(ctx, since, topN)
	if err != nil {
		return nil, err
	}
	return &DashboardSummary{
		WindowDays:    int(window.Hours() / 24),
		Since:         since,
		Counts:        counts,
		FlaggedGauge:  flagged,
		SoonGauge:     soon,
		ArchivedGauge: archived,
		TopAccessed:   top,
	}, nil
}
