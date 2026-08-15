package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// VerifyCorpusPresence probes for --skip-ingest's (task 4.2) expected corpus
// before scoring: one aggregate query counting distinct bench subcategories
// already ingested for tenantID among the slice's question IDs, compared
// against the slice's expected count. A mismatch — the whole corpus missing,
// or a different dataset/seed/n leaving a different question set — fails
// loudly instead of letting scoring silently read an incomplete corpus.
func VerifyCorpusPresence(ctx context.Context, db *gorm.DB, tenantID uuid.UUID, slice []Instance) error {
	expected := make([]string, len(slice))
	for i, inst := range slice {
		expected[i] = CanonicalSegment(inst.QuestionID)
	}

	var found int64
	err := db.WithContext(ctx).Model(&models.Document{}).
		Where("tenant_id = ?", tenantID).
		Where("category = ?", benchCategory).
		Where("archived_at IS NULL").
		Where("subcategory IN ?", expected).
		Distinct("subcategory").
		Count(&found).Error
	if err != nil {
		return fmt.Errorf("corpus presence probe: %w", err)
	}

	if int(found) != len(expected) {
		return fmt.Errorf(
			"--skip-ingest: expected corpus for %d questions under tenant %s category %q, found %d — "+
				"run without --skip-ingest first, or the dataset/seed/n differs from the prior ingest",
			len(expected), tenantID, benchCategory, found,
		)
	}
	return nil
}
