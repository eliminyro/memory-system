package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// RetentionRepository evicts expired, unpinned documents, reusing the
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
// (expiration_age_days) from the effective policy set. A doc_type with expiration
// disabled (0) or not prunable is omitted, so it is never a candidate.
func BuildRetentionCutoffs(policies map[string]models.EffectivePolicy) map[string]int {
	out := make(map[string]int, len(policies))
	for dt, p := range policies {
		if p.ExpirationAgeDays > 0 && p.Prunable {
			out[dt] = p.ExpirationAgeDays
		}
	}
	return out
}

// BuildArchiveCutoffs derives the per-doc_type archive grace in days
// (expiration_age_days) for NON-prunable types from the effective policy set. A
// type with expiration disabled (0) or prunable is omitted, so it never archives.
func BuildArchiveCutoffs(policies map[string]models.EffectivePolicy) map[string]int {
	out := make(map[string]int, len(policies))
	for dt, p := range policies {
		if p.ExpirationAgeDays > 0 && !p.Prunable {
			out[dt] = p.ExpirationAgeDays
		}
	}
	return out
}

// candidateSQL selects a tenant's documents created longer ago than the doc_type
// window — perishables expire from creation, with no grace and no access reprieve —
// excluding pinned and archived docs.
const candidateSQL = `
	SELECT d.id, d.doc_type, d.category, d.subcategory, d.slug
	FROM documents d
	WHERE d.tenant_id = ?
	  AND d.doc_type = ?
	  AND d.archived_at IS NULL
	  AND d.pinned = false
	  AND d.created_at < NOW() - make_interval(days => ?)
	ORDER BY d.created_at ASC
`

// Candidates returns the tenant's eviction candidates per the created_at predicate.
// cutoffs maps a doc_type to its expiration_age_days (see BuildRetentionCutoffs);
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

// ArchiveResult records one archived document; returned so a later phase can count
// archivals without re-querying (the archived gauge surfaces the metric directly).
type ArchiveResult struct {
	ID      uuid.UUID
	DocType string
	Path    string
}

// archiveCandidateSQL selects a tenant's non-prunable docs with no fresh section
// left: every section is flagged and past flagged_at + grace (the EXISTS clause
// requires at least one such section, so a section-less doc is never a candidate).
const archiveCandidateSQL = `
	SELECT d.id, d.doc_type, d.category, d.subcategory, d.slug
	FROM documents d
	WHERE d.tenant_id = ?
	  AND d.doc_type = ?
	  AND d.archived_at IS NULL
	  AND d.pinned = false
	  AND EXISTS (
		SELECT 1 FROM sections s
		WHERE s.document_id = d.id
		  AND s.flagged_at IS NOT NULL
		  AND s.flagged_at + make_interval(days => ?) <= NOW()
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM sections s
		WHERE s.document_id = d.id
		  AND (s.flagged_at IS NULL OR s.flagged_at + make_interval(days => ?) > NOW())
	  )
	ORDER BY d.created_at ASC
`

// ArchiveCandidates returns the tenant's archive candidates per archiveCandidateSQL.
// cutoffs maps a non-prunable doc_type to its expiration_age_days grace (see
// BuildArchiveCutoffs); a doc_type absent from the map is skipped.
func (r *RetentionRepository) ArchiveCandidates(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]int) ([]ArchiveResult, error) {
	type row struct {
		ID          uuid.UUID `gorm:"column:id"`
		DocType     string    `gorm:"column:doc_type"`
		Category    string    `gorm:"column:category"`
		Subcategory *string   `gorm:"column:subcategory"`
		Slug        string    `gorm:"column:slug"`
	}
	var out []ArchiveResult
	for docType, days := range cutoffs {
		if days <= 0 {
			continue
		}
		var rows []row
		if err := r.db.WithContext(ctx).Raw(archiveCandidateSQL, tenantID, docType, days, days).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("archive candidates (%s): %w", docType, err)
		}
		for _, rw := range rows {
			out = append(out, ArchiveResult{
				ID:      rw.ID,
				DocType: rw.DocType,
				Path:    models.BuildPath(rw.Category, rw.Subcategory, rw.Slug),
			})
		}
	}
	return out, nil
}

// ArchiveExpiredStale archives every candidate via ArchiveByID, best-effort and
// idempotent, one transaction each so a failure keeps prior archivals. The returned
// slice counts docs this call actually archived.
func (r *RetentionRepository) ArchiveExpiredStale(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]int) ([]ArchiveResult, error) {
	candidates, err := r.ArchiveCandidates(ctx, tenantID, cutoffs)
	if err != nil {
		return nil, err
	}
	archived := make([]ArchiveResult, 0, len(candidates))
	for _, c := range candidates {
		ok, err := r.archiveOne(ctx, tenantID, c)
		if err != nil {
			return archived, fmt.Errorf("archive %s: %w", c.Path, err)
		}
		if ok {
			archived = append(archived, c)
		}
	}
	return archived, nil
}

// archiveOne archives one document and records a deletion_events audit row (reason
// stale, ArchivedAt set to mark content preserved) in a single transaction. The
// ArchiveByID guard makes it idempotent: a raced archive is a 0-row no-op, skipped.
func (r *RetentionRepository) archiveOne(ctx context.Context, tenantID uuid.UUID, c ArchiveResult) (bool, error) {
	var archived bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, aerr := NewDocumentRepository(tx).ArchiveByID(ctx, c.ID, models.ArchiveReasonStale)
		if aerr != nil {
			return fmt.Errorf("archive document: %w", aerr)
		}
		if n == 0 {
			return nil
		}
		now := time.Now()
		event := &models.DeletionEvent{
			TenantID:     tenantID,
			DocumentPath: c.Path,
			DocType:      c.DocType,
			Reason:       models.ArchiveReasonStale,
			ArchivedAt:   &now,
		}
		if err := tx.WithContext(ctx).Create(event).Error; err != nil {
			return fmt.Errorf("record archive event: %w", err)
		}
		archived = true
		return nil
	})
	return archived, err
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
			Message:      fmt.Sprintf("past its %d-day expiration window (doc_type %s); the sweep would evict it", cutoffs[c.DocType], c.DocType),
		})
	}
	return findings, nil
}
