package models

import (
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Category    string    `gorm:"size:50;not null;uniqueIndex:idx_doc_path" json:"category"`
	Subcategory *string   `gorm:"size:100;uniqueIndex:idx_doc_path" json:"subcategory,omitempty"`
	Slug        string    `gorm:"size:100;not null;uniqueIndex:idx_doc_path" json:"slug"`
	Title       string    `gorm:"size:500;not null" json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	Sections []Section `gorm:"foreignKey:DocumentID;constraint:OnDelete:CASCADE" json:"sections,omitempty"`
}

func (Document) TableName() string {
	return "documents"
}

// Path returns the hierarchical path like "learnings/go/gorm"
func (d Document) Path() string {
	if d.Subcategory != nil {
		return d.Category + "/" + *d.Subcategory + "/" + d.Slug
	}
	return d.Category + "/" + d.Slug
}
