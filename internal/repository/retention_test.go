package repository_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestBuildRetentionCutoffs covers task 6.1: a prunable doc_type with a positive
// expiration_age_days yields exactly that window (no grace addend); non-prunable
// or expiration-disabled types are omitted, so they are never eviction candidates.
func TestBuildRetentionCutoffs(t *testing.T) {
	cutoffs := repository.BuildRetentionCutoffs(map[string]models.EffectivePolicy{
		"journal":   {Prunable: true, ExpirationAgeDays: 30},
		"handoff":   {Prunable: true, ExpirationAgeDays: 90},
		"reference": {Prunable: false, ExpirationAgeDays: 0},
		"learning":  {Prunable: false, ExpirationAgeDays: 180}, // non-prunable → omitted
		"prompt":    {Prunable: true, ExpirationAgeDays: 0},    // prunable but disabled
	})

	require.Equal(t, map[string]int{"journal": 30, "handoff": 90}, cutoffs)
}
