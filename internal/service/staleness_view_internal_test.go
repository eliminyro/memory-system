package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// viewStore serves the learning doc_type with the given clocks (no DB).
func viewStore(verification, expiration int) *staleness.PolicyStore {
	return staleness.NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		models.DocTypeLearning: {VerificationAgeDays: verification, ExpirationAgeDays: expiration},
	})
}

func sectionAged(days int, heading *string, content string) models.Section {
	v := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return models.Section{ID: uuid.New(), Heading: heading, Content: content, VerifiedAt: &v}
}

func TestSectionViewFromModel_Tiering(t *testing.T) {
	ctx := context.Background()
	store := viewStore(30, 60)
	head := "The Heading"

	// fresh: no status, content served.
	fresh, err := sectionViewFromModel(ctx, store, sectionAged(5, &head, "body"), models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Empty(t, fresh.Status)
	require.Equal(t, "body", fresh.Content)

	// needs_verification: content still served, verification threshold reported.
	nv, err := sectionViewFromModel(ctx, store, sectionAged(45, &head, "body"), models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Equal(t, "needs_verification", nv.Status)
	require.Equal(t, "body", nv.Content)
	require.Equal(t, 30, nv.ThresholdDays)

	// expired (hard): body withheld, heading preview, expiration threshold reported.
	exp, err := sectionViewFromModel(ctx, store, sectionAged(90, &head, "body"), models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Equal(t, "expired", exp.Status)
	require.Empty(t, exp.Content, "expired body is withheld")
	require.Equal(t, head, exp.Preview)
	require.Equal(t, 60, exp.ThresholdDays)

	// advisory never withholds even past expiration age.
	adv, err := sectionViewFromModel(ctx, store, sectionAged(90, &head, "body"), models.DocTypeLearning, models.StalenessModeAdvisory, false)
	require.NoError(t, err)
	require.Equal(t, "needs_verification", adv.Status)
	require.Equal(t, "body", adv.Content)
}

func TestSectionViewFromModel_ContentFlag(t *testing.T) {
	ctx := context.Background()
	store := viewStore(30, 60)
	head := "The Heading"
	reason := "internal/foo.go"
	now := time.Now()

	// A fresh (within age) section that carries the content flag reads as
	// needs_verification with its reason — the flag drives it, not age.
	sec := sectionAged(5, &head, "body")
	sec.FlaggedAt, sec.FlagReason = &now, &reason
	flagged, err := sectionViewFromModel(ctx, store, sec, models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Equal(t, "needs_verification", flagged.Status)
	require.Equal(t, reason, flagged.FlagReason)
	require.Equal(t, "body", flagged.Content, "a flag never withholds content")

	// An unflagged fresh section has no status: age within threshold never flags.
	clean, err := sectionViewFromModel(ctx, store, sectionAged(5, &head, "body"), models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Empty(t, clean.Status)
	require.Empty(t, clean.FlagReason)

	// An expired (hard) section that is also flagged: the expired withhold wins
	// the status, but the flag reason is still surfaced.
	exp := sectionAged(90, &head, "body")
	exp.FlaggedAt, exp.FlagReason = &now, &reason
	ev, err := sectionViewFromModel(ctx, store, exp, models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Equal(t, "expired", ev.Status)
	require.Empty(t, ev.Content)
	require.Equal(t, reason, ev.FlagReason)
}

// TestSectionViewFromModel_AdminForceReadPeeks: adminForceRead reveals an expired
// body; without it the body stays withheld.
func TestSectionViewFromModel_AdminForceReadPeeks(t *testing.T) {
	ctx := context.Background()
	store := viewStore(30, 60)
	sec := sectionAged(90, nil, "secret body")

	withheld, err := sectionViewFromModel(ctx, store, sec, models.DocTypeLearning, models.StalenessModeHard, false)
	require.NoError(t, err)
	require.Equal(t, "expired", withheld.Status)
	require.Empty(t, withheld.Content)

	peek, err := sectionViewFromModel(ctx, store, sec, models.DocTypeLearning, models.StalenessModeHard, true)
	require.NoError(t, err)
	require.Equal(t, "secret body", peek.Content, "admin force_read reveals the expired body")
	// Still past the verification age, so the nudge remains — it just is not withheld.
	require.Equal(t, "needs_verification", peek.Status)
}

func TestHeadingPreview(t *testing.T) {
	head := "  A Heading  "
	require.Equal(t, "A Heading", headingPreview(&head, "long body text"))

	empty := "   "
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	got := headingPreview(&empty, string(long))
	require.LessOrEqual(t, len(got), 80+len("…"), "headingless fallback is bounded to ~80 chars")
	require.Equal(t, headingPreview(nil, string(long)), got, "nil and blank heading both fall back")
}

// TestApplyStaleness_ExpiredBlanksBody mirrors the view-builder tiering on the
// search overlay: an expired result is blanked to a heading preview under hard mode.
func TestApplyStaleness_ExpiredBlanksBody(t *testing.T) {
	store := viewStore(30, 60)
	tHard := uuid.New()
	head := "Result Heading"
	old := time.Now().Add(-90 * 24 * time.Hour)
	results := []repository.SearchResult{
		{SectionID: uuid.New(), TenantID: tHard, Heading: &head, Content: "body", VerifiedAt: &old, DocType: models.DocTypeLearning},
	}
	out, err := applyStalenessToSearchResults(context.Background(), store, results, map[uuid.UUID]string{tHard: models.StalenessModeHard}, false)
	require.NoError(t, err)
	require.Equal(t, "expired", out[0].Status)
	require.Empty(t, out[0].Content)
	require.Equal(t, head, out[0].Preview)
	require.Equal(t, 60, out[0].ThresholdDays)
}
