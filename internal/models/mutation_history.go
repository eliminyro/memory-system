package models

import (
	"time"

	"github.com/google/uuid"
)

// Mutation op-type constants for MutationHistory.OpType (keep <= 32 chars).
const (
	MutationOpCreate         = "create"
	MutationOpOverwrite      = "overwrite"
	MutationOpUpdateSection  = "update_section"
	MutationOpUpdateTitle    = "update_title"
	MutationOpDeleteSection  = "delete_section"
	MutationOpDeleteDocument = "delete_document"
)

// MutationHistory is an append-only audit row recording one document mutation:
// who changed what and — for overwrites/deletes — what it said before (JSON in
// Before). Written only when the global toggle is on, shared tenants only; pruned by age.
type MutationHistory struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID     uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	DocumentID   uuid.UUID  `gorm:"type:uuid;not null;index:idx_mutation_history_doc_time,priority:1" json:"document_id"`
	SectionID    *uuid.UUID `gorm:"type:uuid" json:"section_id,omitempty"`
	DocumentPath string     `gorm:"type:text" json:"document_path"`
	OpType       string     `gorm:"size:32;not null" json:"op_type"`
	ActorSubject string     `gorm:"size:255" json:"actor_subject"`
	ActorEmail   *string    `gorm:"type:text" json:"actor_email,omitempty"`
	APIKeyID     *uuid.UUID `gorm:"type:uuid" json:"api_key_id,omitempty"`
	Before       *string    `gorm:"type:text" json:"before,omitempty"`
	CreatedAt    time.Time  `gorm:"index:idx_mutation_history_doc_time,priority:2,sort:desc" json:"created_at"`
}

func (MutationHistory) TableName() string { return "mutation_history" }
