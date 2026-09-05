package service

import (
	"context"
	"time"

	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// MetricsService turns the metric_events log and live section state into bounded,
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
	MetricEventsTotal     = "memory_events_total"
	MetricStaleSections   = "memory_stale_sections"
	MetricExpiredSections = "memory_expired_sections"

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
// live stale/expired gauges, with label cardinality bounded to tenant × doc_type
// (× event_type for counters).
func (m *MetricsService) PrometheusSeries(ctx context.Context, window time.Duration) ([]Series, error) {
	since := time.Now().Add(-window)
	counters, err := m.events.CounterCounts(ctx, since)
	if err != nil {
		return nil, err
	}
	stale, expired, err := m.gauges(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(counters)+len(stale)+len(expired))
	for _, c := range counters {
		out = append(out, Series{
			Name: MetricEventsTotal, Type: seriesTypeCounter,
			Labels: map[string]string{"tenant": c.TenantID.String(), "doc_type": c.DocType, "event_type": c.EventType},
			Value:  float64(c.Count),
		})
	}
	out = append(out, gaugeSeries(MetricStaleSections, stale)...)
	out = append(out, gaugeSeries(MetricExpiredSections, expired)...)
	return out, nil
}

func gaugeSeries(name string, counts []repository.StalenessCount) []Series {
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

// gauges computes current stale and expired section counts (tenant × doc_type) from
// the effective policy windows. Stale is raw corpus health (all tenants, past the
// verification age); expired is hard-mode tenants only, matching read-gating.
func (m *MetricsService) gauges(ctx context.Context) (stale, expired []repository.StalenessCount, err error) {
	verificationDays := make(map[string]int)
	expirationDays := make(map[string]int)
	for dt, p := range m.policies.All() {
		verificationDays[dt] = p.VerificationAgeDays
		expirationDays[dt] = p.ExpirationAgeDays
	}
	if stale, err = m.sections.CountStaleByTenant(ctx, verificationDays); err != nil {
		return nil, nil, err
	}
	if expired, err = m.sections.CountExpiredByTenant(ctx, expirationDays); err != nil {
		return nil, nil, err
	}
	return stale, expired, nil
}

// DashboardSummary is the admin dashboard payload: event counts over a window, the
// live stale/expired gauges, and the top-accessed documents (per-doc detail from
// the event log, allowed here since it is not a Prometheus label).
type DashboardSummary struct {
	WindowDays   int                         `json:"window_days"`
	Since        time.Time                   `json:"since"`
	Counts       []repository.MetricCounter  `json:"counts"`
	StaleGauge   []repository.StalenessCount `json:"stale_sections"`
	ExpiredGauge []repository.StalenessCount `json:"expired_sections"`
	TopAccessed  []repository.TopAccessedDoc `json:"top_accessed"`
}

// DashboardSummary assembles the admin summary over window, listing the topN
// most-accessed documents. Phase D's GET /api/admin/metrics calls this.
func (m *MetricsService) DashboardSummary(ctx context.Context, window time.Duration, topN int) (*DashboardSummary, error) {
	since := time.Now().Add(-window)
	counts, err := m.events.CounterCounts(ctx, since)
	if err != nil {
		return nil, err
	}
	stale, expired, err := m.gauges(ctx)
	if err != nil {
		return nil, err
	}
	top, err := m.events.TopAccessed(ctx, since, topN)
	if err != nil {
		return nil, err
	}
	return &DashboardSummary{
		WindowDays:   int(window.Hours() / 24),
		Since:        since,
		Counts:       counts,
		StaleGauge:   stale,
		ExpiredGauge: expired,
		TopAccessed:  top,
	}, nil
}
