package models

import (
	"time"

	"github.com/google/uuid"
)

// Metric event kinds — the append-only usage events recorded per-tenant when
// metrics_enabled. Counters aggregate over these; stale/expired stay live COUNTs.
const (
	MetricEventAccess  = "access"
	MetricEventVerify  = "verify"
	MetricEventCleanup = "cleanup"
)

// MetricEvent is an append-only per-tenant usage event (access/verify/cleanup),
// written best-effort off the critical path and pruned at metrics_retention_days.
// Indexed on (tenant_id, created_at) and (event_type, created_at) — see Migrate.
type MetricEvent struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID  uuid.UUID  `gorm:"type:uuid;not null" json:"tenant_id"`
	EventType string     `gorm:"size:16;not null" json:"event_type"`
	DocID     *uuid.UUID `gorm:"type:uuid" json:"doc_id,omitempty"`
	DocType   string     `gorm:"size:32" json:"doc_type,omitempty"`
	CreatedAt time.Time  `gorm:"not null;default:now()" json:"created_at"`
}

func (MetricEvent) TableName() string { return "metric_events" }
