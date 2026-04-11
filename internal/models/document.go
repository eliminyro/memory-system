package models

import (
	"strings"
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

// ParsePath splits a hierarchical path into category, subcategory, and slug.
// Handles 3-part ("learnings/go/gorm"), 2-part ("preferences/style"),
// and 1-part ("slug") paths. Single-part paths default to category "misc".
func ParsePath(path string) (category string, subcategory *string, slug string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "misc", nil, path
	}
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 3:
		return parts[0], &parts[1], parts[2]
	case 2:
		return parts[0], nil, parts[1]
	default:
		return "misc", nil, path
	}
}

// Path returns the hierarchical path like "learnings/go/gorm"
func (d Document) Path() string {
	return BuildPath(d.Category, d.Subcategory, d.Slug)
}
