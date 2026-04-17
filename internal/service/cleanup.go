package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// GetCleanupQueue returns unresolved cleanup queue entries for the tenant.
// When includeResolved is true, all rows are returned (most recent first).
func (s *MemoryService) GetCleanupQueue(ctx context.Context, limit int, includeResolved bool, overrideID *uuid.UUID) ([]models.CleanupQueue, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	if s.cleanup == nil {
		return nil, fmt.Errorf("%w: cleanup queue not wired", apperr.ErrInvalidInput)
	}
	if includeResolved {
		return s.cleanup.ListAll(ctx, tid, limit)
	}
	return s.cleanup.ListPending(ctx, tid, limit)
}

// MarkCleanupDone resolves a queue entry. mergedInto is used when resolution
// is "merged" to record which doc survived.
func (s *MemoryService) MarkCleanupDone(ctx context.Context, queueID uuid.UUID, resolution, note string, mergedInto *uuid.UUID, overrideID *uuid.UUID) error {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return err
	}
	if s.cleanup == nil {
		return fmt.Errorf("%w: cleanup queue not wired", apperr.ErrInvalidInput)
	}
	switch resolution {
	case models.CleanupResolutionMerged, models.CleanupResolutionIgnored, models.CleanupResolutionFalsePositive:
	default:
		return fmt.Errorf("%w: resolution must be merged, ignored, or false_positive", apperr.ErrInvalidInput)
	}
	return s.cleanup.Resolve(ctx, tid, queueID, resolution, note, mergedInto)
}

// MergeResult describes what happened during a merge: the surviving doc's
// post-merge view and the section-id → new-ordinal mapping for client bookkeeping.
type MergeResult struct {
	Winner *DocumentView `json:"winner"`
}

// MergeDocuments moves sections from the loser into the winner per the caller's
// sections_to_keep list, deletes unwanted sections, and deletes the loser. The
// caller is responsible for ordering sections_to_keep in the desired final order.
//
// Any section ID in sections_to_keep must belong to either winner or loser; a
// stranger ID rejects the whole operation.
func (s *MemoryService) MergeDocuments(
	ctx context.Context,
	winnerID, loserID uuid.UUID,
	sectionsToKeep []uuid.UUID,
	overrideID *uuid.UUID,
) (*MergeResult, error) {
	if winnerID == loserID {
		return nil, fmt.Errorf("%w: winner and loser must differ", apperr.ErrInvalidInput)
	}
	if len(sectionsToKeep) == 0 {
		return nil, fmt.Errorf("%w: sections_to_keep is required", apperr.ErrInvalidInput)
	}

	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	// Dedup while preserving order.
	seen := make(map[uuid.UUID]struct{}, len(sectionsToKeep))
	ordered := make([]uuid.UUID, 0, len(sectionsToKeep))
	for _, id := range sectionsToKeep {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txDocs := repository.NewDocumentRepository(tx)
		txSections := repository.NewSectionRepository(tx)

		winner, err := txDocs.GetByID(ctx, tid, winnerID)
		if err != nil {
			return fmt.Errorf("load winner: %w", err)
		}
		loser, err := txDocs.GetByID(ctx, tid, loserID)
		if err != nil {
			return fmt.Errorf("load loser: %w", err)
		}
		if winner.TenantID != tid || loser.TenantID != tid {
			return fmt.Errorf("%w: cannot merge across tenants", apperr.ErrInvalidInput)
		}

		// Build membership: which doc does each section belong to?
		ownership := map[uuid.UUID]uuid.UUID{}
		var allSections []models.Section
		if err := tx.Where("document_id IN ?", []uuid.UUID{winnerID, loserID}).Find(&allSections).Error; err != nil {
			return fmt.Errorf("load sections: %w", err)
		}
		for _, sec := range allSections {
			ownership[sec.ID] = sec.DocumentID
		}

		// Validate every keep ID belongs to one of the two docs.
		for _, id := range ordered {
			owner, ok := ownership[id]
			if !ok || (owner != winnerID && owner != loserID) {
				return fmt.Errorf("%w: section %s not in winner or loser", apperr.ErrInvalidInput, id)
			}
		}

		// Reassign ordinals and move loser-side sections into winner.
		for i, id := range ordered {
			updates := map[string]any{
				"ordinal":     i,
				"document_id": winnerID,
			}
			if err := tx.Model(&models.Section{}).
				Where("id = ?", id).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("update section %s: %w", id, err)
			}
		}

		// Delete sections that weren't kept.
		if err := tx.Where("document_id IN ? AND id NOT IN ?", []uuid.UUID{winnerID, loserID}, ordered).
			Delete(&models.Section{}).Error; err != nil {
			return fmt.Errorf("delete dropped sections: %w", err)
		}

		// Delete the loser document. No sections reference it anymore.
		if err := txSections.DeleteByDocumentID(ctx, loserID); err != nil {
			return fmt.Errorf("delete loser sections (safety): %w", err)
		}
		if err := txDocs.Delete(ctx, tid, loserID); err != nil {
			return fmt.Errorf("delete loser doc: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Reload the winner for the response.
	postMerge, err := s.docs.GetByID(ctx, tid, winnerID)
	if err != nil {
		return nil, fmt.Errorf("reload winner: %w", err)
	}
	// Post-merge view uses the caller's tenant settings for staleness; force-read
	// is true because the caller just authored the merge — no need to refuse it.
	settings := s.tenantSettings(ctx, tid)
	view, err := buildDocumentView(ctx, s.thresholds, postMerge, settings.StalenessMode, true)
	if err != nil {
		return nil, err
	}
	return &MergeResult{Winner: &view}, nil
}
