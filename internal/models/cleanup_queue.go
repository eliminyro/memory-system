package models

import (
	"time"

	"github.com/google/uuid"
)

// Cleanup resolution constants.
const (
	CleanupResolutionMerged        = "merged"
	CleanupResolutionIgnored       = "ignored"
	CleanupResolutionFalsePositive = "false_positive"
)

// CleanupQueue holds near-duplicate candidates detected by the nightly lint
// scan. A client-side scheduled agent pulls pending rows, performs LLM-assisted
// merges, and marks them resolved.
type CleanupQueue struct {
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID       uuid.UUID  `gorm:"type:uuid;not null;index:idx_cleanup_pending,priority:1" json:"tenant_id"`
	DocAID         uuid.UUID  `gorm:"type:uuid;not null" json:"doc_a_id"`
	DocBID         uuid.UUID  `gorm:"type:uuid;not null" json:"doc_b_id"`
	Similarity     float64    `gorm:"not null" json:"similarity"`
	DetectedAt     time.Time  `gorm:"not null;default:NOW()" json:"detected_at"`
	ResolvedAt     *time.Time `gorm:"index:idx_cleanup_pending,priority:2" json:"resolved_at,omitempty"`
	Resolution     *string    `gorm:"size:32" json:"resolution,omitempty"`
	ResolutionNote *string    `gorm:"type:text" json:"resolution_note,omitempty"`
	MergedInto     *uuid.UUID `gorm:"type:uuid" json:"merged_into,omitempty"`
}

func (CleanupQueue) TableName() string { return "cleanup_queue" }
