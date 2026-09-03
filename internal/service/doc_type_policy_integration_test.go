//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

func strp(s string) *string { return &s }

// TestJournalMergeWriteMode covers the merge_sections write mode: an incoming
// section adds, a matching heading replaces (no duplicate), and identical content
// is idempotent — the data-loss hole the change closes (spec §Write mode).
func TestJournalMergeWriteMode(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	const slug = "2026-09-01"

	_, err := f.svc.StoreDocument(ctx, "journal", nil, slug, "## 09:15\nmorning", false, "", nil, nil)
	require.NoError(t, err)
	_, err = f.svc.StoreDocument(ctx, "journal", nil, slug, "## 14:30\nafternoon", false, "", nil, nil)
	require.NoError(t, err)

	view, err := f.svc.GetDocument(ctx, "journal", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 2, "merge preserves the earlier section")

	// Replace a matching heading in place — no duplicate.
	_, err = f.svc.StoreDocument(ctx, "journal", nil, slug, "## 09:15\nmorning-v2", false, "", nil, nil)
	require.NoError(t, err)
	view, err = f.svc.GetDocument(ctx, "journal", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 2, "replacing a heading must not add a section")
	require.Contains(t, sectionContents(view), "morning-v2")

	// Idempotent: re-sending identical content changes nothing.
	_, err = f.svc.StoreDocument(ctx, "journal", nil, slug, "## 09:15\nmorning-v2", false, "", nil, nil)
	require.NoError(t, err)
	view, err = f.svc.GetDocument(ctx, "journal", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 2)
}

func sectionContents(v *service.DocumentView) []string {
	out := make([]string, 0, len(v.Sections))
	for _, s := range v.Sections {
		out = append(out, s.Content)
	}
	return out
}

// TestIdentityValidation covers slug_format and subcategory rules, and that a
// rejected write leaves nothing behind (spec §Identity validation, §Write path order).
func TestIdentityValidation(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	// Malformed journal slug rejected; nothing persisted.
	_, err := f.svc.StoreDocument(ctx, "journal", nil, "sept-1", "## H\nbody", false, "", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
	_, err = f.svc.GetDocument(ctx, "journal", nil, "sept-1", false, "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "a rejected write must leave no document")

	// Well-formed journal slug proceeds.
	_, err = f.svc.StoreDocument(ctx, "journal", nil, "2026-09-02", "## H\nbody", false, "", nil, nil)
	require.NoError(t, err)

	// journal forbids a subcategory.
	_, err = f.svc.StoreDocument(ctx, "journal", strp("work"), "2026-09-03", "## H\nbody", false, "", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)

	// handoff requires a subcategory.
	_, err = f.svc.StoreDocument(ctx, "handoffs", nil, "h1", "## H\nbody", false, "", nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
	_, err = f.svc.StoreDocument(ctx, "handoffs", strp("proj"), "h2", "## H\nbody", false, "", nil, nil)
	require.NoError(t, err)
}

// TestDefaultSearchExclusion covers that journals drop out of an unfiltered search
// but rank when the query names their category (spec §Embedding and default search).
func TestDefaultSearchExclusion(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "zebracrossing"

	_, err := f.svc.StoreDocument(ctx, "journal", nil, "2026-09-05", "## H\n"+token+" entry", false, "", nil, nil)
	require.NoError(t, err)
	_, err = f.svc.StoreDocument(ctx, "learnings", nil, "note-"+token, "## H\n"+token+" learning", false, "", nil, nil)
	require.NoError(t, err)

	unfiltered, err := f.svc.Search(ctx, token, nil, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	for _, r := range unfiltered {
		require.NotEqual(t, models.DocTypeJournal, r.DocType, "journal must be absent from an unfiltered search")
	}
	require.NotEmpty(t, unfiltered, "the learning with the token is still returned")

	cat := "journal"
	filtered, err := f.svc.Search(ctx, token, &cat, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	var sawJournal bool
	for _, r := range filtered {
		if r.DocType == models.DocTypeJournal {
			sawJournal = true
		}
	}
	require.True(t, sawJournal, "a category-filtered query still finds the journal")
}

// TestAdminPolicyWrite covers admin gating, immediate effect after recompute, the
// audit entry, and merged-result validation (spec §Rules edited only by admins).
func TestAdminPolicyWrite(t *testing.T) {
	f := newAuthzFixture(t)
	adminCtx := ctxFor(f.tenantA, f.admin)
	memberCtx := ctxFor(f.tenantA, f.subjA)

	// A non-admin is refused.
	require.Error(t, f.svc.SetDocTypePolicy(memberCtx, models.DocTypePolicy{DocType: models.DocTypeLearning, VerificationAgeDays: iptrLocal(5)}))

	// Admin write takes effect immediately (visible in the read).
	require.NoError(t, f.svc.SetDocTypePolicy(adminCtx, models.DocTypePolicy{DocType: models.DocTypeLearning, VerificationAgeDays: iptrLocal(5)}))
	_, eff, err := f.svc.ListDocTypePolicies(adminCtx)
	require.NoError(t, err)
	require.Equal(t, 5, eff[models.DocTypeLearning].VerificationAgeDays)

	// Audited to override_log.
	var n int64
	require.NoError(t, f.db.Model(&models.OverrideLog{}).
		Where("override_type = ?", models.OverrideTypePolicyChange).Count(&n).Error)
	require.Positive(t, n)

	// A merged-invalid write is rejected (default_search true needs embed).
	require.Error(t, f.svc.SetDocTypePolicy(adminCtx, models.DocTypePolicy{
		DocType: models.DocTypeReference, DefaultSearch: bptrLocal(true), Embed: bptrLocal(false),
	}))
}

// TestPutSection covers add-without-read, replace-by-heading, document creation,
// slug validation, and that a replace-mode type keeps its other sections (spec §put_section).
func TestPutSection(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	slug := "put-" + randToken()

	// Creates the document holding one section.
	_, err := f.svc.PutSection(ctx, "learnings", nil, slug, "First", "one", nil)
	require.NoError(t, err)
	view, err := f.svc.GetDocument(ctx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 1)

	// Adds a second heading without a prior read; other section survives (replace-mode type).
	_, err = f.svc.PutSection(ctx, "learnings", nil, slug, "Second", "two", nil)
	require.NoError(t, err)
	view, err = f.svc.GetDocument(ctx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 2, "replace-mode type keeps its other sections under put_section")

	// Replaces by heading, no duplicate.
	_, err = f.svc.PutSection(ctx, "learnings", nil, slug, "First", "one-v2", nil)
	require.NoError(t, err)
	view, err = f.svc.GetDocument(ctx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Len(t, view.Sections, 2)
	require.Contains(t, sectionContents(view), "one-v2")

	// Same slug_format validation as store_memory.
	_, err = f.svc.PutSection(ctx, "journal", nil, "sept-1", "H", "body", nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

func iptrLocal(i int) *int   { return &i }
func bptrLocal(b bool) *bool { return &b }
func randToken() string      { return uuid.NewString()[:8] }
