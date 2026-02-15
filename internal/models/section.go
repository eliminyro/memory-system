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
	Embedding  pgvector.Vector `gorm:"type:vector(1024)" json:"-"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`

	Document *Document `gorm:"foreignKey:DocumentID" json:"document,omitempty"`
}

func (Section) TableName() string {
	return "sections"
}
