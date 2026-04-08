package models

import (
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID    uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000001';index" json:"tenant_id"`
	Category    string    `gorm:"size:50;not null" json:"category"`
	Subcategory *string   `gorm:"size:100" json:"subcategory,omitempty"`
	Slug        string    `gorm:"size:100;not null" json:"slug"`
	Title       string    `gorm:"size:500;not null" json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	Tenant   *Tenant   `gorm:"foreignKey:TenantID" json:"-"`
	Sections []Section `gorm:"foreignKey:DocumentID;constraint:OnDelete:CASCADE" json:"sections,omitempty"`
}

func (Document) TableName() string {
	return "documents"
}

// BuildPath constructs a hierarchical path from category/subcategory/slug.
func BuildPath(category string, subcategory *string, slug string) string {
	if subcategory != nil {
		return category + "/" + *subcategory + "/" + slug
	}
	return category + "/" + slug
}

// Path returns the hierarchical path like "learnings/go/gorm"
func (d Document) Path() string {
	return BuildPath(d.Category, d.Subcategory, d.Slug)
}
