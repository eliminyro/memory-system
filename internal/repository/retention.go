package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
)

// retentionAdvisoryLock derives a stable int64 lock key from a tenant UUID so
// per-tenant retention sweeps serialize across replicas via pg_advisory_xact_lock.
func retentionAdvisoryLock(id uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(id[:8]))
}

// RetentionRepository runs the retention sweep SQL: archive expired docs and
// hard-delete docs past the archive grace window. No LLM calls.
type RetentionRepository struct {
	db *gorm.DB
}

func NewRetentionRepository(db *gorm.DB) *RetentionRepository {
	return &RetentionRepository{db: db}
}

// ArchiveExpired sets archived_at on docs whose freshest section is older than
// the doc_type cutoff. "Freshest" = latest of verified_at/updated_at/created_at,
// so editing a section (bumps updated_at, not verified_at) keeps a doc alive, not
// just mark_verified. One UPDATE per doc_type.
//
// Zero-section fallback: a doc with NO sections satisfies NOT EXISTS trivially and
// would otherwise be archived on its very next sweep regardless of its own age. The
// extra guard makes the zero-section case fall back to the document's own
// created_at/updated_at, so a freshly-created empty doc is not archived until it is
// itself older than the cutoff. Docs that have sections are unaffected.
func (r *RetentionRepository) ArchiveExpired(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]time.Time) (int64, error) {
	// Episodic doc_types (journal, ...) are excluded even though the caller is
	// expected to have already partitioned them out — belt-and-suspenders so
	// nothing ever archives them; their retention path is direct-delete only.
	episodic := pq.StringArray(models.EpisodicDocTypes())
	const sql = `
		UPDATE documents d
		SET archived_at = now()
		WHERE d.tenant_id = ?
		  AND d.doc_type = ?
		  AND d.doc_type <> ALL(?)
		  AND d.archived_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM sections s
			WHERE s.document_id = d.id
			  AND GREATEST(COALESCE(s.verified_at, s.created_at), s.updated_at) >= ?
		  )
		  AND (
			EXISTS (SELECT 1 FROM sections s2 WHERE s2.document_id = d.id)
			OR GREATEST(d.updated_at, d.created_at) < ?
		  )
	`
	var total int64
	for docType, cutoff := range cutoffs {
		res := r.db.WithContext(ctx).Exec(sql, tenantID, docType, episodic, cutoff, cutoff)
		if res.Error != nil {
			return total, fmt.Errorf("archive expired (%s): %w", docType, res.Error)
		}
		total += res.RowsAffected
	}
	return total, nil
}

// DeleteArchived hard-deletes docs archived before `before` in one atomic
// statement: writes a deletion_events audit row per victim, purges cleanup_queue
// rows referencing a victim, prunes the victims' relation_tuples, then deletes
// the docs (sections cascade). Returns count deleted.
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
		),
		tuples AS (
			DELETE FROM relation_tuples
			WHERE object_type = ? AND object_id IN (SELECT id::text FROM victims)
		)
		DELETE FROM documents WHERE id IN (SELECT id FROM victims)
	`
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize concurrent retention sweeps of THIS tenant across replicas
		// (the scanner runs per-replica). A tx-scoped advisory lock is held to
		// COMMIT; the second sweep then sees the first's committed deletes (no
		// victims) and writes no duplicate deletion_events audit rows.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", retentionAdvisoryLock(tenantID)).Error; err != nil {
			return err
		}
		res := tx.Exec(sql, tenantID, before, tenantID, authz.TypeDocument)
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete archived: %w", err)
	}
	return deleted, nil
}

// DeleteEpisodicExpired hard-deletes episodic docs (journal, ...) once past
// their retention window, in place of the archive-then-delete path: same
// liveness math (incl. zero-section fallback) as ArchiveExpired, and the same
// deletion_events audit + cleanup_queue/relation_tuples cascade as
// DeleteArchived — kept as a parallel implementation (not a shared helper) so
// this path can't perturb the tested archive-then-delete one. archived_at is
// always NULL for these victims (episodic docs are never archived), which is
// itself the audit-trail signal distinguishing a direct-delete from an
// archive-then-delete row. One statement per doc_type in cutoffs, doubly
// scoped by both the exact doc_type and the episodic set.
func (r *RetentionRepository) DeleteEpisodicExpired(ctx context.Context, tenantID uuid.UUID, cutoffs map[string]time.Time) (int64, error) {
	episodic := pq.StringArray(models.EpisodicDocTypes())
	const sql = `
		WITH victims AS (
			SELECT d.id, d.doc_type, d.archived_at,
			       d.category || '/' || COALESCE(d.subcategory || '/', '') || d.slug AS path
			FROM documents d
			WHERE d.tenant_id = ?
			  AND d.doc_type = ?
			  AND d.doc_type = ANY(?)
			  AND d.archived_at IS NULL
			  AND NOT EXISTS (
				SELECT 1 FROM sections s
				WHERE s.document_id = d.id
				  AND GREATEST(COALESCE(s.verified_at, s.created_at), s.updated_at) >= ?
			  )
			  AND (
				EXISTS (SELECT 1 FROM sections s2 WHERE s2.document_id = d.id)
				OR GREATEST(d.updated_at, d.created_at) < ?
			  )
		),
		audit AS (
			INSERT INTO deletion_events (tenant_id, document_path, doc_type, reason, archived_at, deleted_at)
			SELECT ?, path, doc_type, ?, archived_at, now() FROM victims
		),
		purge AS (
			DELETE FROM cleanup_queue
			WHERE doc_a_id IN (SELECT id FROM victims)
			   OR doc_b_id IN (SELECT id FROM victims)
		),
		tuples AS (
			DELETE FROM relation_tuples
			WHERE object_type = ? AND object_id IN (SELECT id::text FROM victims)
		)
		DELETE FROM documents WHERE id IN (SELECT id FROM victims)
	`
	var total int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Same per-tenant advisory lock as DeleteArchived, held for the whole
		// loop so concurrent replica sweeps serialize across all doc_types.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", retentionAdvisoryLock(tenantID)).Error; err != nil {
			return err
		}
		for docType, cutoff := range cutoffs {
			res := tx.Exec(sql,
				tenantID, docType, episodic, cutoff, cutoff,
				tenantID, models.DeletionReasonRetention,
				authz.TypeDocument,
			)
			if res.Error != nil {
				return fmt.Errorf("delete episodic expired (%s): %w", docType, res.Error)
			}
			total += res.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete episodic expired: %w", err)
	}
	return total, nil
}
