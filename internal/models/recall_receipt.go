package models

import (
	"time"

	"github.com/google/uuid"
)

// RecallReceipt records what a search served, so a later report_recall_outcome
// call can credit hit_count/miss_count on the sections it named. One row per
// non-empty search (design D3); pruned by TTL, never touching the counts it
// already credited on sections.
type RecallReceipt struct {
	RecallID   uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"recall_id"`
	TenantID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	SectionIDs UUIDArray  `gorm:"type:uuid[];not null" json:"section_ids"`
	CreatedAt  time.Time  `json:"created_at"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

func (RecallReceipt) TableName() string { return "recall_receipts" }

// Recall outcome enum for report_recall_outcome (design D4).
const (
	RecallOutcomeSuccess = "success"
	RecallOutcomeFailure = "failure"
)

// ValidRecallOutcomes is the accepted outcome set.
var ValidRecallOutcomes = map[string]struct{}{
	RecallOutcomeSuccess: {},
	RecallOutcomeFailure: {},
}
