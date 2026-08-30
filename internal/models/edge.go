package models

import (
	"time"

	"github.com/google/uuid"
)

// Edge types name the directed, typed relationship a document asserts to another.
const (
	EdgeSupersedes    = "supersedes"
	EdgeDerivedFrom   = "derived_from"
	EdgeRelatesTo     = "relates_to"
	EdgeContinuesFrom = "continues_from"
)

// ValidEdgeTypes lists all accepted edge_type values (mirrors ValidDocTypes).
var ValidEdgeTypes = map[string]struct{}{
	EdgeSupersedes:    {},
	EdgeDerivedFrom:   {},
	EdgeRelatesTo:     {},
	EdgeContinuesFrom: {},
}

// Edge is a directed, typed doc-to-doc relationship. Both endpoints share a
// tenant (v1); the FK cascade drops the edge when either endpoint is hard-deleted,
// while archiving (which keeps the row) leaves the edge — the supersede trail.
type Edge struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID         uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	SourceDocumentID uuid.UUID `gorm:"type:uuid;not null;index" json:"source_document_id"`
	TargetDocumentID uuid.UUID `gorm:"type:uuid;not null;index" json:"target_document_id"`
	EdgeType         string    `gorm:"size:32;not null" json:"edge_type"`
	ActorSubject     string    `gorm:"size:255" json:"actor_subject"`
	CreatedAt        time.Time `json:"created_at"`

	// Association fields exist only so AutoMigrate emits the OnDelete:CASCADE FKs.
	Source *Document `gorm:"foreignKey:SourceDocumentID;constraint:OnDelete:CASCADE" json:"-"`
	Target *Document `gorm:"foreignKey:TargetDocumentID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Edge) TableName() string { return "document_edges" }
