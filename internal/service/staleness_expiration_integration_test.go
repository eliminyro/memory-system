//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// backdateFirstSection ages a document's first section's verified_at.
func backdateFirstSection(t *testing.T, f *authzFixture, secID uuid.UUID, days int) {
	t.Helper()
	old := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	require.NoError(t, f.db.Model(&models.Section{}).Where("id = ?", secID).Update("verified_at", old).Error)
}

// TestExpiration_HardWithholdPeekAndVerify covers the friction contract: a
// non-admin sees an expired section withheld and cannot force-read it; an admin
// peek reveals it once without resetting the clock; mark_verified unlocks it.
func TestExpiration_HardWithholdPeekAndVerify(t *testing.T) {
	f := newAuthzFixture(t)
	memberCtx := ctxFor(f.tenantA, f.subjA)
	adminCtx := ctxFor(f.tenantA, f.admin)

	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)
	require.NoError(t, f.svc.SetDocTypePolicy(adminCtx,
		models.DocTypePolicy{DocType: models.DocTypeLearning, ExpirationAgeDays: iptrLocal(200)}))

	res, err := f.svc.StoreDocument(memberCtx, "learnings", nil,
		"exp-"+uuid.NewString(), "# T\n\n## Heading\nthe secret body", true, "seed", nil, nil)
	require.NoError(t, err)
	slug := res.Document.Slug
	secID := res.Document.Sections[0].ID
	backdateFirstSection(t, f, secID, 400)

	// Non-admin read: withheld.
	v, err := f.svc.GetDocument(memberCtx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, "expired", v.Sections[0].Status)
	require.Empty(t, v.Sections[0].Content)

	// Non-admin force_read: still withheld (no bypass for non-admins).
	v, err = f.svc.GetDocument(memberCtx, "learnings", nil, slug, true, "non-admin peek", nil)
	require.NoError(t, err)
	require.Equal(t, "expired", v.Sections[0].Status, "non-admin force_read must not reveal an expired body")
	require.Empty(t, v.Sections[0].Content)

	// Admin force_read: reveals the body once.
	va, err := f.svc.GetDocument(adminCtx, "learnings", nil, slug, true, "admin break-glass", nil)
	require.NoError(t, err)
	require.Contains(t, va.Sections[0].Content, "the secret body", "admin peek reveals the content")

	// The admin peek did NOT reset the clock: a following non-admin read is still expired.
	v, err = f.svc.GetDocument(memberCtx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, "expired", v.Sections[0].Status, "admin peek leaves the section expired for the next reader")

	// mark_verified unlocks it for everyone and is audited.
	require.NoError(t, f.svc.MarkVerified(memberCtx, secID, nil))
	v, err = f.svc.GetDocument(memberCtx, "learnings", nil, slug, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, v.Sections[0].Status, "verified section is served fresh")
	require.Contains(t, v.Sections[0].Content, "the secret body")

	var n int64
	require.NoError(t, f.db.Model(&models.OverrideLog{}).
		Where("override_type = ?", models.OverrideTypeVerification).Count(&n).Error)
	require.Positive(t, n, "mark_verified writes an audit row")
}

// TestExpiration_AdvisoryNeverWithholds: advisory mode past the expiration age
// serves content with a needs_verification nudge and never withholds.
func TestExpiration_AdvisoryNeverWithholds(t *testing.T) {
	f := newAuthzFixture(t)
	memberCtx := ctxFor(f.tenantA, f.subjA)
	adminCtx := ctxFor(f.tenantA, f.admin)

	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeAdvisory).Error)
	require.NoError(t, f.svc.SetDocTypePolicy(adminCtx,
		models.DocTypePolicy{DocType: models.DocTypeLearning, VerificationAgeDays: iptrLocal(180), ExpirationAgeDays: iptrLocal(200)}))
	t.Cleanup(func() {
		require.NoError(t, f.svc.SetDocTypePolicy(adminCtx,
			models.DocTypePolicy{DocType: models.DocTypeLearning, ExpirationAgeDays: iptrLocal(0)}))
	})

	res, err := f.svc.StoreDocument(memberCtx, "learnings", nil,
		"adv-"+uuid.NewString(), "# T\n\n## Heading\nadvisory body", true, "seed", nil, nil)
	require.NoError(t, err)
	backdateFirstSection(t, f, res.Document.Sections[0].ID, 400)

	v, err := f.svc.GetDocument(memberCtx, "learnings", nil, res.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, "needs_verification", v.Sections[0].Status, "advisory nudges, never withholds")
	require.Contains(t, v.Sections[0].Content, "advisory body")
	require.Equal(t, 180, v.Sections[0].ThresholdDays, "advisory reports the verification age, not expiration")
}
