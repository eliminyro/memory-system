package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// RetentionRepository evicts expired, access-cold, unpinned documents, reusing the
// delete-document purge cascade (sections + embeddings + FTS + edges).
type RetentionRepository struct {
	db *gorm.DB
}

func NewRetentionRepository(db *gorm.DB) *RetentionRepository {
	return &RetentionRepository{db: db}
}

// RetentionCandidate identifies one document eligible for retention eviction.
type RetentionCandidate struct {
	ID      uuid.UUID
	DocType string
	Path    string
}

// RetentionDeletion records one evicted document; returned so a later phase can
// emit cleanup metrics and count evictions without re-querying.
type RetentionDeletion struct {
	ID      uuid.UUID
	DocType string
	Path    string
}

// BuildRetentionCutoffs derives the per-doc_type eviction window in days
// (expiration_age_days + grace) from the effective policy set. A doc_type with
// expiration disabled (0) or not prunable is omitted, so it is never a candidate.
func BuildRetentionCutoffs(policies map[string]models.EffectivePolicy, grace int) map[string]int {
	if grace < 0 {
		grace = 0
	}
	out := make(map[string]int, len(policies))
	for dt, p := range policies {
		if p.ExpirationAgeDays > 0 && p.Prunable {
			out[dt] = p.ExpirationAgeDays + grace
		}
	}
	return out
}

// candidateSQL selects a tenant's documents whose liveness clock —
// GREATEST(last access, newest section verification, creation) — is older than the
// doc_type window, excluding pinned and archived docs.
const candidateSQL = `
	SELECT d.id, d.doc_type, d.category, d.subcategory, d.slug
	FROM documents d
	WHERE d.tenant_id = ?
	  AND d.doc_type = ?
	  AND d.archived_at IS NULL
	  AND d.pinned = false
	  AND GREATEST(
	        COALESCE(d.last_accessed_at, d.created_at),
	        d.created_at,
	        COALESCE((SELECT MAX(COALESCE(s.verified_at, s.created_at))
	                    FROM sections s WHERE s.document_id = d.id), d.created_at)
	      ) < NOW() - make_interval(days => ?)
	ORDER BY d.created_at ASC
`

// Candidates returns the tenant's eviction candidates per the liveness predicate.
// cutoffs maps a doc_type to expiration_age_days+grace (see BuildRetentionCutoffs);
// a doc_type absent from the map is skipped, so expiration=0 types are never touched.
func (r *RetentionRepository) Candidates(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]int) ([]RetentionCandidate, error) {
	type row struct {
		ID          uuid.UUID `gorm:"column:id"`
		DocType     string    `gorm:"column:doc_type"`
		Category    string    `gorm:"column:category"`
		Subcategory *string   `gorm:"column:subcategory"`
		Slug        string    `gorm:"column:slug"`
	}
	var out []RetentionCandidate
	for docType, days := range cutoffs {
		if days <= 0 {
			continue
		}
		var rows []row
		if err := r.db.WithContext(ctx).Raw(candidateSQL, tenantID, docType, days).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("retention candidates (%s): %w", docType, err)
		}
		for _, rw := range rows {
			out = append(out, RetentionCandidate{
				ID:      rw.ID,
				DocType: rw.DocType,
				Path:    models.BuildPath(rw.Category, rw.Subcategory, rw.Slug),
			})
		}
	}
	return out, nil
}

// DeleteExpiredCold hard-deletes every candidate via the delete-document purge
// cascade, one transaction each (with its deletion_events audit row) so a failure
// keeps prior evictions. The returned slice feeds later metrics.
func (r *RetentionRepository) DeleteExpiredCold(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]int) ([]RetentionDeletion, error) {
	candidates, err := r.Candidates(ctx, tenantID, cutoffs)
	if err != nil {
		return nil, err
	}
	deleted := make([]RetentionDeletion, 0, len(candidates))
	for _, c := range candidates {
		if err := r.evict(ctx, tenantID, c); err != nil {
			return deleted, fmt.Errorf("retention evict %s: %w", c.Path, err)
		}
		deleted = append(deleted, RetentionDeletion(c))
	}
	return deleted, nil
}

// evict purges one document and records its deletion in a single transaction,
// reusing the delete-document cascade (sections repo purges embeddings + FTS,
// the documents delete cascades edges via the FK).
func (r *RetentionRepository) evict(ctx context.Context, tenantID uuid.UUID, c RetentionCandidate) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := NewSectionRepository(tx).DeleteByDocumentID(ctx, c.ID); err != nil {
			return fmt.Errorf("delete sections: %w", err)
		}
		if err := NewDocumentRepository(tx).Delete(ctx, tenantID, c.ID); err != nil {
			return fmt.Errorf("delete document: %w", err)
		}
		event := &models.DeletionEvent{
			TenantID:     tenantID,
			DocumentPath: c.Path,
			DocType:      c.DocType,
			Reason:       models.DeletionReasonRetention,
		}
		if err := tx.WithContext(ctx).Create(event).Error; err != nil {
			return fmt.Errorf("record deletion event: %w", err)
		}
		return nil
	})
}

// CandidateFindings shapes the eviction candidates as lint findings for the
// retention dry-run: it runs the candidate SELECT only, never deletes, and is
// independent of the sweep toggle.
func (r *RetentionRepository) CandidateFindings(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]int) ([]LintFinding, error) {
	candidates, err := r.Candidates(ctx, tenantID, cutoffs)
	if err != nil {
		return nil, err
	}
	findings := make([]LintFinding, 0, len(candidates))
	for _, c := range candidates {
		findings = append(findings, LintFinding{
			Check:        "retention_candidate",
			Severity:     LintSeverityWarning,
			DocumentPath: c.Path,
			Message:      fmt.Sprintf("expired and access-cold past its %d-day retention window (doc_type %s); the sweep would evict it", cutoffs[c.DocType], c.DocType),
		})
	}
	return findings, nil
}
