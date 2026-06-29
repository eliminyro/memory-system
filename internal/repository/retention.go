package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RetentionRepository runs the retention sweep's SQL: archive expired docs and
// hard-delete docs past the archive grace window. No LLM calls (mirrors the
// dedup scanner's repository).
type RetentionRepository struct {
	db *gorm.DB
}

func NewRetentionRepository(db *gorm.DB) *RetentionRepository {
	return &RetentionRepository{db: db}
}

// ArchiveExpired sets archived_at on documents whose freshest section is older
// than the cutoff for their doc_type. A document is a candidate only when NO
// section has been touched on/after the cutoff, where "touched" is the latest
// of verified_at, updated_at, or created_at — so editing a section (which bumps
// updated_at but not verified_at) keeps its document alive, not just an explicit
// mark_verified. One UPDATE per doc_type keeps the per-type cutoff binding simple.
func (r *RetentionRepository) ArchiveExpired(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]time.Time) (int64, error) {
	const sql = `
		UPDATE documents d
		SET archived_at = now()
		WHERE d.tenant_id = ?
		  AND d.doc_type = ?
		  AND d.archived_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM sections s
			WHERE s.document_id = d.id
			  AND GREATEST(COALESCE(s.verified_at, s.created_at), s.updated_at) >= ?
		  )
	`
	var total int64
	for docType, cutoff := range cutoffs {
		res := r.db.WithContext(ctx).Exec(sql, tenantID, docType, cutoff)
		if res.Error != nil {
			return total, fmt.Errorf("archive expired (%s): %w", docType, res.Error)
		}
		total += res.RowsAffected
	}
	return total, nil
}

// DeleteArchived hard-deletes documents archived before `before`, in one
// atomic statement: it records a deletion_events audit row per victim, purges
// any cleanup_queue rows referencing a victim (no orphans), then deletes the
// documents (sections cascade via FK). Returns the number of documents deleted.
func (r *RetentionRepository) DeleteArchived(ctx context.Context, tenantID uuid.UUID, before time.Time) (int64, error) {
	const sql = `
		WITH victims AS (
			SELECT id, doc_type, archived_at,
			       category || '/' || COALESCE(subcategory || '/', '') || slug AS path
			FROM documents
			WHERE tenant_id = ?
			  AND archived_at IS NOT NULL
			  AND archived_at < ?
		),
		audit AS (
			INSERT INTO deletion_events (tenant_id, document_path, doc_type, reason, archived_at, deleted_at)
			SELECT ?, path, doc_type, 'retention_sweep', archived_at, now() FROM victims
		),
		purge AS (
			DELETE FROM cleanup_queue
			WHERE doc_a_id IN (SELECT id FROM victims)
			   OR doc_b_id IN (SELECT id FROM victims)
		)
		DELETE FROM documents WHERE id IN (SELECT id FROM victims)
	`
	res := r.db.WithContext(ctx).Exec(sql, tenantID, before, tenantID)
	if res.Error != nil {
		return 0, fmt.Errorf("delete archived: %w", res.Error)
	}
	return res.RowsAffected, nil
}
