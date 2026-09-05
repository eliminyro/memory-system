package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// MetricEventRepository appends, aggregates, and prunes the metric_events log —
// the per-tenant usage events (access/verify/cleanup) behind the metrics layer.
type MetricEventRepository struct {
	db *gorm.DB
}

func NewMetricEventRepository(db *gorm.DB) *MetricEventRepository {
	return &MetricEventRepository{db: db}
}

// Append inserts one usage event. Callers invoke it best-effort off the critical
// path, so an error is returned for logging only, never to fail the underlying op.
func (r *MetricEventRepository) Append(ctx context.Context, ev *models.MetricEvent) error {
	if err := r.db.WithContext(ctx).Create(ev).Error; err != nil {
		return fmt.Errorf("append metric event: %w", err)
	}
	return nil
}

// MetricCounter is one bounded-cardinality counter cell: the event count for a
// tenant × doc_type × event_type over a window.
type MetricCounter struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	DocType   string    `json:"doc_type"`
	EventType string    `json:"event_type"`
	Count     int64     `json:"count"`
}

// CounterCounts aggregates events at/after since into the Prometheus counter shape:
// grouped by tenant × doc_type × event_type.
func (r *MetricEventRepository) CounterCounts(ctx context.Context, since time.Time) ([]MetricCounter, error) {
	var out []MetricCounter
	err := r.db.WithContext(ctx).
		Model(&models.MetricEvent{}).
		Select("tenant_id, doc_type, event_type, COUNT(*) AS count").
		Where("created_at >= ?", since).
		Group("tenant_id, doc_type, event_type").
		Order("tenant_id, doc_type, event_type").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("counter counts: %w", err)
	}
	return out, nil
}

// TopAccessedDoc is one most-accessed document over a window. Per-document detail
// lives only here (never a Prometheus label); Path/Title are empty when the doc has
// since been deleted (the LEFT JOIN finds no row).
type TopAccessedDoc struct {
	DocID    uuid.UUID `json:"doc_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	DocType  string    `json:"doc_type"`
	Path     string    `json:"path,omitempty"`
	Title    string    `json:"title,omitempty"`
	Count    int64     `json:"count"`
}

// TopAccessed returns the limit most-accessed documents (by access-event count) at/
// after since, LEFT JOINing documents for the current path/title.
func (r *MetricEventRepository) TopAccessed(ctx context.Context, since time.Time, limit int) ([]TopAccessedDoc, error) {
	if limit <= 0 {
		limit = 10
	}
	type row struct {
		DocID       uuid.UUID
		TenantID    uuid.UUID
		DocType     string
		Category    string
		Subcategory *string
		Slug        string
		Title       string
		Count       int64
	}
	var rows []row
	err := r.db.WithContext(ctx).
		Table("metric_events AS me").
		Select(`me.doc_id AS doc_id, me.tenant_id AS tenant_id, me.doc_type AS doc_type,
			COALESCE(d.category, '') AS category, d.subcategory,
			COALESCE(d.slug, '') AS slug, COALESCE(d.title, '') AS title,
			COUNT(*) AS count`).
		Joins("LEFT JOIN documents d ON d.id = me.doc_id").
		Where("me.event_type = ? AND me.doc_id IS NOT NULL AND me.created_at >= ?", models.MetricEventAccess, since).
		Group("me.doc_id, me.tenant_id, me.doc_type, d.category, d.subcategory, d.slug, d.title").
		Order("count DESC, me.doc_id").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("top accessed: %w", err)
	}
	out := make([]TopAccessedDoc, 0, len(rows))
	for _, rw := range rows {
		path := ""
		if rw.Category != "" {
			path = models.BuildPath(rw.Category, rw.Subcategory, rw.Slug)
		}
		out = append(out, TopAccessedDoc{
			DocID: rw.DocID, TenantID: rw.TenantID, DocType: rw.DocType,
			Path: path, Title: rw.Title, Count: rw.Count,
		})
	}
	return out, nil
}

// PruneOlderThan hard-deletes events created before cutoff, returning the count —
// the metrics_retention_days bound applied by the maintenance sweep.
func (r *MetricEventRepository) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&models.MetricEvent{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune metric events: %w", res.Error)
	}
	return res.RowsAffected, nil
}
