package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// SectionView is the API-facing projection of a section. A needs_verification
// section keeps its content (a nudge); an expired section (hard mode) has its
// body withheld and carries a heading-based Preview instead.
type SectionView struct {
	ID            uuid.UUID  `json:"id"`
	DocumentID    uuid.UUID  `json:"document_id"`
	Ordinal       int        `json:"ordinal"`
	Heading       *string    `json:"heading,omitempty"`
	Content       string     `json:"content,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Status        string     `json:"status,omitempty"`
	Preview       string     `json:"preview,omitempty"`
	StaleDays     int        `json:"age_days,omitempty"`
	ThresholdDays int        `json:"threshold_days,omitempty"`
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

	// Populated only on an expand read: the resolved included documents (flat,
	// ordered, de-duplicated) and a per-edge resolution manifest.
	Includes        []DocumentView `json:"includes,omitempty"`
	IncludeManifest []IncludeRef   `json:"include_manifest,omitempty"`
}

// buildDocumentView applies the staleness filter to each section per the tenant's
// mode. store nil or mode "off" passes content through. adminForceRead reveals an
// expired body (admin break-glass, no clock reset); non-admins map to false.
func buildDocumentView(ctx context.Context, store *staleness.PolicyStore, doc *models.Document, mode string, adminForceRead bool) (DocumentView, error) {
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
	view.Sections = make([]SectionView, 0, len(doc.Sections))
	for _, sec := range doc.Sections {
		sv, err := sectionViewFromModel(ctx, store, sec, doc.DocType, mode, adminForceRead)
		if err != nil {
			return DocumentView{}, err
		}
		view.Sections = append(view.Sections, sv)
	}
	return view, nil
}

func sectionViewFromModel(ctx context.Context, store *staleness.PolicyStore, sec models.Section, docType, mode string, adminForceRead bool) (SectionView, error) {
	view := SectionView{
		ID:         sec.ID,
		DocumentID: sec.DocumentID,
		Ordinal:    sec.Ordinal,
		Heading:    sec.Heading,
		VerifiedAt: sec.VerifiedAt,
		CreatedAt:  sec.CreatedAt,
		UpdatedAt:  sec.UpdatedAt,
	}
	if store == nil || mode == models.StalenessModeOff {
		view.Content = sec.Content
		return view, nil
	}
	check := staleness.Check(store, sec, docType, mode)
	// Expired (hard mode) withholds the body unless an admin peeks; heading preview only.
	if check.Expired && !adminForceRead {
		view.Status = "expired"
		view.StaleDays = int(check.Age / (24 * time.Hour))
		view.ThresholdDays = check.ExpirationDays
		view.Preview = headingPreview(sec.Heading, sec.Content)
		return view, nil
	}
	if check.Stale {
		view.Status = "needs_verification"
		view.StaleDays = int(check.Age / (24 * time.Hour))
		view.ThresholdDays = check.VerificationDays
	}
	view.Content = sec.Content
	return view, nil
}

// headingPreview orients a caller on a withheld section: its heading verbatim, or
// a short bounded leading-text prefix when it has no heading. Query-independent.
func headingPreview(heading *string, content string) string {
	if heading != nil && strings.TrimSpace(*heading) != "" {
		return strings.TrimSpace(*heading)
	}
	return staleness.Preview(content, 80)
}

// applyStalenessToSearchResults overlays staleness metadata per each result's OWN
// owning-tenant mode (modeByTenant, keyed by TenantID; absent/"off" is untouched).
// Hard-mode expired blanks the body to a heading preview unless adminForceRead.
func applyStalenessToSearchResults(ctx context.Context, store *staleness.PolicyStore, results []repository.SearchResult, modeByTenant map[uuid.UUID]string, adminForceRead bool) ([]repository.SearchResult, error) {
	if store == nil {
		return results, nil
	}
	for i := range results {
		r := &results[i]
		mode := modeByTenant[r.TenantID]
		if mode == "" || mode == models.StalenessModeOff {
			continue
		}
		check := staleness.Check(store, models.Section{
			Content:    r.Content,
			VerifiedAt: r.VerifiedAt,
			CreatedAt:  r.SectionCreated,
		}, r.DocType, mode)
		r.StaleDays = int(check.Age / (24 * time.Hour))
		if check.Expired && !adminForceRead {
			r.Status = "expired"
			r.ThresholdDays = check.ExpirationDays
			r.Preview = headingPreview(r.Heading, r.Content)
			r.Content = ""
			continue
		}
		if check.Stale {
			r.Status = "needs_verification"
			r.ThresholdDays = check.VerificationDays
		}
	}
	return results, nil
}
