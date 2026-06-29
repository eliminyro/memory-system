package models

import (
	"time"

	"github.com/google/uuid"
)

// DeletionEvent is an append-only audit row written when a document is hard-
// deleted (currently only by the retention sweep). Kept forever — cheap rows,
// and the provenance is needed to answer "what was removed and when".
type DeletionEvent struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID     uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	DocumentPath string     `gorm:"type:text;not null" json:"document_path"`
	DocType      string     `gorm:"size:32" json:"doc_type,omitempty"`
	Reason       string     `gorm:"size:32;not null" json:"reason"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
	DeletedAt    time.Time  `gorm:"not null;default:now()" json:"deleted_at"`
}

func (DeletionEvent) TableName() string { return "deletion_events" }

// DeletionReasonRetention is the reason recorded for retention-sweep deletions.
const DeletionReasonRetention = "retention_sweep"
