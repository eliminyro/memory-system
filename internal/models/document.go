package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// DocType enumerates document kinds for staleness threshold lookup.
// Agents pick one when storing; mapped automatically from category for legacy docs.
const (
	DocTypeProjectState = "project_state"
	DocTypeAudit        = "audit"
	DocTypeLearning     = "learning"
	DocTypePreference   = "preference"
	DocTypeTool         = "tool"
	DocTypeReference    = "reference"
	DocTypeJournal      = "journal"
	DocTypeHandoff      = "handoff"
	DocTypePrompt       = "prompt"
)

// ValidDocTypes lists all accepted doc_type values.
var ValidDocTypes = map[string]struct{}{
	DocTypeProjectState: {},
	DocTypeAudit:        {},
	DocTypeLearning:     {},
	DocTypePreference:   {},
	DocTypeTool:         {},
	DocTypeReference:    {},
	DocTypeJournal:      {},
	DocTypeHandoff:      {},
	DocTypePrompt:       {},
}

type Document struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID    uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000001';index" json:"tenant_id"`
	Category    string    `gorm:"size:50;not null" json:"category"`
	Subcategory *string   `gorm:"size:100" json:"subcategory,omitempty"`
	Slug        string    `gorm:"size:100;not null" json:"slug"`
	Title       string    `gorm:"size:500;not null" json:"title"`
	DocType     string    `gorm:"size:32;not null;default:'reference';index" json:"doc_type"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// ArchivedAt marks a document retired by the retention sweep. Non-NULL =
	// excluded from all reads; hard-deleted after the delete grace period.
	ArchivedAt *time.Time `gorm:"index:idx_documents_archived_at" json:"archived_at,omitempty"`
	// ArchiveReason names the lifecycle rule that archived the doc (empty until
	// archived); carried into the deletion audit at hard-delete.
	ArchiveReason string `gorm:"size:32" json:"archive_reason,omitempty"`
	// LastAccessedAt is bumped when a search serves the doc's sections;
	// COALESCE(last_accessed_at, created_at) drives access-recency eviction (D1).
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	// Pinned exempts the doc from access-based eviction regardless of age (D4).
	Pinned bool `gorm:"not null;default:false" json:"pinned"`
	// ContentHash is hex(sha256(raw markdown)); powers the write-guard exact-dup
	// short-circuit. Nullable/unbackfilled — pre-migration docs fall to the centroid.
	ContentHash string `gorm:"size:64;index" json:"-"`

	// Scope gates a document's applicability: empty = always applies, non-empty = a
	// space-separated pattern list matched against a read-time scope (drives
	// conditional includes). Allowed on any doc_type.
	Scope *string `gorm:"column:scope;size:500" json:"scope,omitempty"`

	// Display-only owning-tenant labels (not columns) — populated by the service
	// for list responses so browse shows the tenant name/type, like search does.
	TenantName string `gorm:"-" json:"tenant_name,omitempty"`
	TenantType string `gorm:"-" json:"tenant_type,omitempty"`

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
// Single-part defaults to category "misc"; 4+ segments map the first to category,
// the last to slug, and the middle (joined by "/") to a multi-segment subcategory.
func ParsePath(path string) (category string, subcategory *string, slug string) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "misc", nil, path
	}
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 1:
		return "misc", nil, parts[0]
	case 2:
		return parts[0], nil, parts[1]
	case 3:
		return parts[0], &parts[1], parts[2]
	default:
		sub := strings.Join(parts[1:len(parts)-1], "/")
		return parts[0], &sub, parts[len(parts)-1]
	}
}

// Path returns the hierarchical path like "learnings/go/gorm"
func (d Document) Path() string {
	return BuildPath(d.Category, d.Subcategory, d.Slug)
}
