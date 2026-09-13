//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// sectionsOf reads a document's sections (ordinal order) directly, so a test can
// assert the persisted needs-verification flag.
func sectionsOf(t *testing.T, f *authzFixture, docID uuid.UUID) []models.Section {
	t.Helper()
	var secs []models.Section
	require.NoError(t, f.db.Where("document_id = ?", docID).Order("ordinal ASC").Find(&secs).Error)
	return secs
}

// sectionsOfErr is the non-failing variant for polled goroutines: it returns the
// query error instead of require-ing, so a transient/teardown DB error retries
// rather than failing the test from inside require.Eventually.
func sectionsOfErr(f *authzFixture, docID uuid.UUID) ([]models.Section, error) {
	var secs []models.Section
	err := f.db.Where("document_id = ?", docID).Order("ordinal ASC").Find(&secs).Error
	return secs, err
}

// storeHinted stores a learnings doc carrying verify_hints and returns its id.
func storeHinted(t *testing.T, f *authzFixture, ctx context.Context, content string, hints ...string) uuid.UUID {
	t.Helper()
	res, err := f.svc.StoreDocumentScoped(ctx, "learnings", nil, "vh-"+uuid.NewString(), content, true, "seed", nil, nil, nil, hints...)
	require.NoError(t, err)
	require.NotNil(t, res.Document)
	return res.Document.ID
}

// TestVerifyHints_FlagChanged covers the flag_changed contract: a matching path
// flags the hinted section, an unrelated path flags nothing, and re-verify clears.
func TestVerifyHints_FlagChanged(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	docID := storeHinted(t, f, ctx, "# T\n\n## H\nbody", "internal/models/section.go:Section")

	// An unrelated path flags nothing.
	n, err := f.svc.FlagChanged(ctx, []string{"internal/other/zzz.go"}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Nil(t, sectionsOf(t, f, docID)[0].FlaggedAt, "non-matching path never flags")

	// The hinted file changes (prefix match on the file part): the section flags.
	n, err = f.svc.FlagChanged(ctx, []string{"internal/models/section.go"}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	secs := sectionsOf(t, f, docID)
	require.NotNil(t, secs[0].FlaggedAt, "hinted file change flags the section")
	require.NotNil(t, secs[0].FlagReason)

	// The read surfaces it (served in full) and re-verify clears it.
	view, err := f.svc.GetDocumentByID(ctx, docID, false, "", nil)
	require.NoError(t, err)
	require.True(t, view.ReviewPending, "doc-level rollup reflects the flagged section")
	require.Equal(t, "needs_verification", view.Sections[0].Status)
	require.NotEmpty(t, view.Sections[0].Content, "a flag never withholds content")

	_, err = f.svc.UpdateSection(ctx, secs[0].ID, nil, nil, true, nil)
	require.NoError(t, err)
	require.Nil(t, sectionsOf(t, f, docID)[0].FlaggedAt, "re-verify clears the flag")
}

// TestVerifyHints_NoHintsNeverFlag confirms a doc with no verify_hints is never
// flagged by flag_changed — the flag is content-driven, never age-driven.
func TestVerifyHints_NoHintsNeverFlag(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "nh-"+uuid.NewString(), "# T\n\n## H\nbody", true, "seed", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID

	n, err := f.svc.FlagChanged(ctx, []string{"internal/models/section.go", "anything/at/all.go"}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Nil(t, sectionsOf(t, f, docID)[0].FlaggedAt, "a hint-less section is never flagged")
}

// TestDependsOn_FlagsAllDependentSections verifies a dependency content change
// flags EVERY section of the dependent, and re-verify clears only that section.
func TestDependsOn_FlagsAllDependentSections(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	depRes, err := f.svc.StoreDocument(ctx, "learnings", nil, "dep-"+uuid.NewString(),
		"# A\n\n## One\nbody one\n\n## Two\nbody two", true, "seed", nil, nil)
	require.NoError(t, err)
	dep := depRes.Document.ID
	require.Len(t, depRes.Document.Sections, 2)

	tgtRes, err := f.svc.StoreDocument(ctx, "learnings", nil, "tgt-"+uuid.NewString(),
		"# B\n\n## H\ntarget body", true, "seed", nil, nil)
	require.NoError(t, err)
	tgtSec := tgtRes.Document.Sections[0].ID
	tgtPath := tgtRes.Path

	_, err = f.svc.CreateEdge(ctx, dep, tgtRes.Document.ID, models.EdgeDependsOn, nil)
	require.NoError(t, err)

	body := "changed target body"
	_, err = f.svc.UpdateSection(ctx, tgtSec, &body, nil, false, nil)
	require.NoError(t, err)

	// Propagation is a detached side-effect: poll until BOTH sections are flagged.
	require.Eventually(t, func() bool {
		secs, err := sectionsOfErr(f, dep)
		if err != nil || len(secs) != 2 {
			return false
		}
		for _, s := range secs {
			if s.FlaggedAt == nil || s.FlagReason == nil || *s.FlagReason != tgtPath {
				return false
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond, "all dependent sections flagged, naming the target")

	// Re-verify ONE section: only that section's flag clears (per-section).
	secs := sectionsOf(t, f, dep)
	_, err = f.svc.UpdateSection(ctx, secs[0].ID, nil, nil, true, nil)
	require.NoError(t, err)
	after := sectionsOf(t, f, dep)
	require.Nil(t, after[0].FlaggedAt, "re-verified section is cleared")
	require.NotNil(t, after[1].FlaggedAt, "the other section stays flagged")
}
