package service

import (
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// SectionView is the API-facing projection of a section. A needs_verification
// section (content/event-flagged) keeps its content — the flag is advisory.
type SectionView struct {
	ID         uuid.UUID  `json:"id"`
	DocumentID uuid.UUID  `json:"document_id"`
	Ordinal    int        `json:"ordinal"`
	Heading    *string    `json:"heading,omitempty"`
	Content    string     `json:"content,omitempty"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Status     string     `json:"status,omitempty"`
	// FlagReason names why this section is content/event-flagged needs-verification
	// (a changed verify_hints path or a depends_on change). Empty = not flagged.
	FlagReason string `json:"flag_reason,omitempty"`
}

// DocumentView is the API-facing projection of a document with filtered sections.
type DocumentView struct {
	ID          uuid.UUID     `json:"id"`
	TenantID    uuid.UUID     `json:"tenant_id"`
	TenantName  string        `json:"tenant_name,omitempty"`
	TenantType  string        `json:"tenant_type,omitempty"`
	Category    string        `json:"category"`
	Subcategory *string       `json:"subcategory,omitempty"`
	Slug        string        `json:"slug"`
	Title       string        `json:"title"`
	DocType     string        `json:"doc_type"`
	Scope       *string       `json:"scope,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Sections    []SectionView `json:"sections,omitempty"`

	// Typed edges to other documents, both directions, read-scope gated. Compact
	// (no content); populated on document reads, omitted when the doc has none.
	Edges []EdgeView `json:"edges,omitempty"`

	// ReviewPending: at least one section is flagged needs-verification (a changed
	// verify_hints path or a depends_on change). Advisory doc-level rollup of the
	// section flags; ReviewReason carries a flagged section's reason.
	ReviewPending bool   `json:"review_pending,omitempty"`
	ReviewReason  string `json:"review_reason,omitempty"`

	// Advisory expiry signals, prunable docs only (nil otherwise): when this doc
	// expires, and same-type siblings expiring within 7 days. Never withhold.
	ExpiresAt        *time.Time          `json:"expires_at,omitempty"`
	ExpiresInDays    *int                `json:"expires_in_days,omitempty"`
	TypeExpiringSoon []ExpiringSoonEntry `json:"type_expiring_soon,omitempty"`

	// Populated only on an expand read: the resolved included documents (flat,
	// ordered, de-duplicated) and a per-edge resolution manifest.
	Includes        []DocumentView `json:"includes,omitempty"`
	IncludeManifest []IncludeRef   `json:"include_manifest,omitempty"`
}

// ExpiringSoonEntry is one same-type sibling nearing expiry: its path and the
// days left before the sweep would evict it.
type ExpiringSoonEntry struct {
	Path          string `json:"path"`
	ExpiresInDays int    `json:"expires_in_days"`
}

// EdgeView is the compact edge projection embedded on a document read: the edge
// type, its direction relative to this doc, and the other endpoint's identity.
type EdgeView struct {
	EdgeType  string `json:"edge_type"`
	Direction string `json:"direction"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Archived  bool   `json:"archived,omitempty"`
}

// buildDocumentView projects a document and its sections, surfacing the
// content/event needs-verification flag and the advisory prunable-expiry signal.
// A nil store skips the expiry advisory.
func buildDocumentView(store *staleness.PolicyStore, doc *models.Document) (DocumentView, error) {
	view := DocumentView{
		ID:          doc.ID,
		TenantID:    doc.TenantID,
		Category:    doc.Category,
		Subcategory: doc.Subcategory,
		Slug:        doc.Slug,
		Title:       doc.Title,
		DocType:     doc.DocType,
		Scope:       doc.Scope,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}
	// Per-doc expiry, prunable types only. Days may be 0/negative (overdue but
	// unswept) — advisory, so don't clamp.
	if store != nil {
		if pol := store.EffectiveFor(doc.DocType); pol.Prunable && pol.ExpirationAgeDays > 0 {
			expiresAt := doc.CreatedAt.AddDate(0, 0, pol.ExpirationAgeDays)
			days := int(time.Until(expiresAt).Hours() / 24)
			view.ExpiresAt = &expiresAt
			view.ExpiresInDays = &days
		}
	}
	view.Sections = make([]SectionView, 0, len(doc.Sections))
	for _, sec := range doc.Sections {
		sv := sectionViewFromModel(sec)
		// Doc-level rollup of the section flags: first flagged section wins the reason.
		if sec.FlaggedAt != nil && !view.ReviewPending {
			view.ReviewPending = true
			if sec.FlagReason != nil {
				view.ReviewReason = *sec.FlagReason
			}
		}
		view.Sections = append(view.Sections, sv)
	}
	return view, nil
}

// sectionViewFromModel projects a section, surfacing the content/event
// needs-verification flag (advisory — content is always served).
func sectionViewFromModel(sec models.Section) SectionView {
	view := SectionView{
		ID:         sec.ID,
		DocumentID: sec.DocumentID,
		Ordinal:    sec.Ordinal,
		Heading:    sec.Heading,
		Content:    sec.Content,
		VerifiedAt: sec.VerifiedAt,
		CreatedAt:  sec.CreatedAt,
		UpdatedAt:  sec.UpdatedAt,
	}
	if sec.FlaggedAt != nil {
		view.Status = "needs_verification"
		if sec.FlagReason != nil {
			view.FlagReason = *sec.FlagReason
		}
	}
	return view
}

// applyFlagStatus overlays the content/event needs-verification status on each
// result carrying a section flag (FlaggedAt); FlagReason rides along from SQL.
func applyFlagStatus(results []repository.SearchResult) {
	for i := range results {
		if results[i].FlaggedAt != nil {
			results[i].Status = "needs_verification"
		}
	}
}
