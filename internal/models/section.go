package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

type Section struct {
	ID         uuid.UUID       `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	DocumentID uuid.UUID       `gorm:"type:uuid;not null;index:idx_section_doc_ord" json:"document_id"`
	Ordinal    int             `gorm:"not null;index:idx_section_doc_ord" json:"ordinal"`
	Heading    *string         `gorm:"size:500" json:"heading,omitempty"`
	Content    string          `gorm:"type:text;not null" json:"content"`
	Embedding  pgvector.Vector `gorm:"type:vector" json:"-"`
	VerifiedAt *time.Time      `gorm:"index:idx_sections_verified_at" json:"verified_at,omitempty"`
	// VerifyHints are file/symbol/line references (file:symbol, file:line) a
	// git-hook matches on change via flag_changed; jsonb array, NULL/[] = none.
	VerifyHints []string `gorm:"serializer:json;type:jsonb" json:"verify_hints,omitempty"`
	// FlaggedAt/FlagReason: the content/event-driven needs-verification flag, set
	// by verify_hints match or a depends_on change, cleared on re-verify.
	FlaggedAt  *time.Time `json:"flagged_at,omitempty"`
	FlagReason *string    `gorm:"size:500" json:"flag_reason,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	Document *Document `gorm:"foreignKey:DocumentID" json:"document,omitempty"`
}

func (Section) TableName() string {
	return "sections"
}
