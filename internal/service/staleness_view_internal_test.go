package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestSectionViewFromModel_ContentFlag: a flagged section reads needs_verification
// with its reason and full content; an unflagged section has no status.
func TestSectionViewFromModel_ContentFlag(t *testing.T) {
	head := "The Heading"
	reason := "internal/foo.go"
	now := time.Now()

	sec := models.Section{ID: uuid.New(), Heading: &head, Content: "body", FlaggedAt: &now, FlagReason: &reason}
	flagged := sectionViewFromModel(sec)
	require.Equal(t, "needs_verification", flagged.Status)
	require.Equal(t, reason, flagged.FlagReason)
	require.Equal(t, "body", flagged.Content, "a flag never withholds content")

	clean := sectionViewFromModel(models.Section{ID: uuid.New(), Heading: &head, Content: "body"})
	require.Empty(t, clean.Status)
	require.Empty(t, clean.FlagReason)
}

// TestApplyFlagStatus_SetsNeedsVerification: a result carrying a section flag is
// overlaid with the needs_verification status; content is untouched.
func TestApplyFlagStatus_SetsNeedsVerification(t *testing.T) {
	now := time.Now()
	head := "Result Heading"
	results := []repository.SearchResult{
		{SectionID: uuid.New(), TenantID: uuid.New(), Heading: &head, Content: "body", FlaggedAt: &now, DocType: models.DocTypeLearning},
		{SectionID: uuid.New(), TenantID: uuid.New(), Content: "clean", DocType: models.DocTypeLearning},
	}
	applyFlagStatus(results)
	require.Equal(t, "needs_verification", results[0].Status)
	require.Equal(t, "body", results[0].Content, "the flag never blanks content")
	require.Empty(t, results[1].Status)
}
