package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// SectionView is the API-facing projection of a section. When the section is
// stale and mentions code paths, Content is replaced with Preview + verify
// hints and Status is set to "needs_verification".
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
	VerifyHints   []string   `json:"verify_hints,omitempty"`
	StaleDays     int        `json:"age_days,omitempty"`
	ThresholdDays int        `json:"threshold_days,omitempty"`
}

// DocumentView is the API-facing projection of a document with filtered sections.
type DocumentView struct {
	ID          uuid.UUID     `json:"id"`
	TenantID    uuid.UUID     `json:"tenant_id"`
	Category    string        `json:"category"`
	Subcategory *string       `json:"subcategory,omitempty"`
	Slug        string        `json:"slug"`
	Title       string        `json:"title"`
	DocType     string        `json:"doc_type"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Sections    []SectionView `json:"sections,omitempty"`
}

// buildDocumentView applies the staleness filter to each section according to
// the tenant's mode. When store is nil (e.g. import CLI) or mode is "off",
// staleness is entirely skipped and content passes through unchanged.
func buildDocumentView(ctx context.Context, store *staleness.ThresholdStore, doc *models.Document, mode string, forceRead bool) (DocumentView, error) {
	view := DocumentView{
		ID:          doc.ID,
		TenantID:    doc.TenantID,
		Category:    doc.Category,
		Subcategory: doc.Subcategory,
		Slug:        doc.Slug,
		Title:       doc.Title,
		DocType:     doc.DocType,
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
	}
	view.Sections = make([]SectionView, 0, len(doc.Sections))
	for _, sec := range doc.Sections {
		sv, err := sectionViewFromModel(ctx, store, sec, doc.DocType, mode, forceRead)
		if err != nil {
			return DocumentView{}, err
		}
		view.Sections = append(view.Sections, sv)
	}
	return view, nil
}

func sectionViewFromModel(ctx context.Context, store *staleness.ThresholdStore, sec models.Section, docType, mode string, forceRead bool) (SectionView, error) {
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
	check, err := staleness.Check(ctx, store, sec, docType)
	if err != nil {
		return SectionView{}, err
	}
	view.ThresholdDays = check.ThresholdDays
	view.StaleDays = int(check.Age / (24 * time.Hour))

	if mode == models.StalenessModeHard && check.Guarded && !forceRead {
		view.Status = "needs_verification"
		view.Preview = staleness.Preview(sec.Content, 200)
		view.VerifyHints = staleness.ExtractVerifyHints(sec.Content, 5)
		return view, nil
	}
	// advisory (or hard-but-not-guarded) — metadata present, content returned.
	view.Content = sec.Content
	return view, nil
}

// applyStalenessToSearchResults overlays staleness metadata on search results.
// In "hard" mode, guarded sections have Content replaced with Preview + hints.
// In "advisory" mode, metadata is set but Content is preserved.
// In "off" mode (or with nil store), this is a no-op.
func applyStalenessToSearchResults(ctx context.Context, store *staleness.ThresholdStore, results []repository.SearchResult, mode string, forceRead bool) ([]repository.SearchResult, error) {
	if store == nil || mode == models.StalenessModeOff {
		return results, nil
	}
	for i := range results {
		r := &results[i]
		check, err := staleness.Check(ctx, store, models.Section{
			Content:    r.Content,
			VerifiedAt: r.VerifiedAt,
			CreatedAt:  r.SectionCreated,
		}, r.DocType)
		if err != nil {
			return nil, err
		}
		r.ThresholdDays = check.ThresholdDays
		r.StaleDays = int(check.Age / (24 * time.Hour))
		if mode == models.StalenessModeHard && check.Guarded && !forceRead {
			r.Status = "needs_verification"
			r.Preview = staleness.Preview(r.Content, 200)
			r.VerifyHints = staleness.ExtractVerifyHints(r.Content, 5)
			r.Content = ""
		}
	}
	return results, nil
}
