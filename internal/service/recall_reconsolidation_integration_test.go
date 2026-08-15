//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// recallOutcomeSectionCounts loads hit/miss counts for a section directly from
// the DB, bypassing the service (which never reads them — Phase A neutrality).
func recallOutcomeSectionCounts(t *testing.T, f *authzFixture, id uuid.UUID) (hit, miss int) {
	t.Helper()
	var s models.Section
	require.NoError(t, f.db.Where("id = ?", id).First(&s).Error)
	return s.HitCount, s.MissCount
}

// TestSearch_RecordsReceiptForNonEmptyResults proves a non-empty search issues
// exactly one receipt naming the served section and returns its recall_id.
func TestSearch_RecordsReceiptForNonEmptyResults(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "recalltok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "recall-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	results, recallID, err := f.svc.Search(ctx, token, nil, nil, 20, false, "", nil)
	require.NoError(t, err)
	require.NotEmpty(t, results, "expected the seeded section to match")
	require.NotEqual(t, uuid.Nil, recallID, "non-empty search must return a recall id")

	var count int64
	require.NoError(t, f.db.Table("recall_receipts").Where("recall_id = ? AND tenant_id = ?", recallID, f.tenantA).Count(&count).Error)
	require.Equal(t, int64(1), count, "expected exactly one receipt")

	var receipt models.RecallReceipt
	require.NoError(t, f.db.Where("recall_id = ?", recallID).First(&receipt).Error)
	require.Contains(t, []uuid.UUID(receipt.SectionIDs), secID, "receipt must name the served section")
}

// TestSearch_EmptyResultsIssueNoReceipt proves a zero-result search returns
// uuid.Nil and creates no receipt row.
func TestSearch_EmptyResultsIssueNoReceipt(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	var before int64
	require.NoError(t, f.db.Table("recall_receipts").Count(&before).Error)

	results, recallID, err := f.svc.Search(ctx, "no-such-token-"+uuid.NewString(), nil, nil, 20, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Equal(t, uuid.Nil, recallID)

	var after int64
	require.NoError(t, f.db.Table("recall_receipts").Count(&after).Error)
	require.Equal(t, before, after, "empty search must not create a receipt")
}

// TestReportRecallOutcome_SuccessCreditsHit proves outcome=success increments
// hit_count (and only hit_count) on the served section.
func TestReportRecallOutcome_SuccessCreditsHit(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "hittok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "hit-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	_, recallID, err := f.svc.Search(ctx, token, nil, nil, 20, false, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, recallID)

	require.NoError(t, f.svc.ReportRecallOutcome(ctx, recallID, models.RecallOutcomeSuccess, nil))

	hit, miss := recallOutcomeSectionCounts(t, f, secID)
	require.Equal(t, 1, hit)
	require.Equal(t, 0, miss)
}

// TestReportRecallOutcome_FailureCreditsMiss mirrors the success case for
// outcome=failure.
func TestReportRecallOutcome_FailureCreditsMiss(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "misstok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "miss-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	_, recallID, err := f.svc.Search(ctx, token, nil, nil, 20, false, "", nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReportRecallOutcome(ctx, recallID, models.RecallOutcomeFailure, nil))

	hit, miss := recallOutcomeSectionCounts(t, f, secID)
	require.Equal(t, 0, hit)
	require.Equal(t, 1, miss)
}

// TestReportRecallOutcome_DuplicateIsNoOp proves a second report for the same
// recall_id does not double-count and does not error.
func TestReportRecallOutcome_DuplicateIsNoOp(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "duptok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "dup-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	_, recallID, err := f.svc.Search(ctx, token, nil, nil, 20, false, "", nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReportRecallOutcome(ctx, recallID, models.RecallOutcomeSuccess, nil))
	require.NoError(t, f.svc.ReportRecallOutcome(ctx, recallID, models.RecallOutcomeSuccess, nil), "duplicate report must not error")

	hit, _ := recallOutcomeSectionCounts(t, f, secID)
	require.Equal(t, 1, hit, "duplicate report must not double-credit")
}

// TestReportRecallOutcome_UnknownRecallID proves an unknown recall_id is
// ErrNotFound.
func TestReportRecallOutcome_UnknownRecallID(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	err := f.svc.ReportRecallOutcome(ctx, uuid.New(), models.RecallOutcomeSuccess, nil)
	require.ErrorIs(t, err, apperr.ErrNotFound)
}

// TestReportRecallOutcome_CrossTenantRejected proves a recall_id issued by one
// tenant cannot be reported by another: ErrNotFound, and no section is credited.
func TestReportRecallOutcome_CrossTenantRejected(t *testing.T) {
	f := newAuthzFixture(t)
	ctxA := ctxFor(f.tenantA, f.subjA)
	ctxB := ctxFor(f.tenantB, f.subjB)
	token := "xtenanttok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctxA, "learnings", nil, "xten-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	_, recallID, err := f.svc.Search(ctxA, token, nil, nil, 20, false, "", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, recallID)

	err = f.svc.ReportRecallOutcome(ctxB, recallID, models.RecallOutcomeSuccess, nil)
	require.ErrorIs(t, err, apperr.ErrNotFound)

	hit, miss := recallOutcomeSectionCounts(t, f, secID)
	require.Equal(t, 0, hit)
	require.Equal(t, 0, miss)
}

// TestReportRecallOutcome_InvalidOutcomeRejected proves an outcome outside
// {success,failure} is rejected as ErrInvalidInput.
func TestReportRecallOutcome_InvalidOutcomeRejected(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	err := f.svc.ReportRecallOutcome(ctx, uuid.New(), "bogus", nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput)
}

// TestReportRecallOutcome_AdminOverrideFindsReceipt proves an admin who
// searches with tenant_id=X can report the outcome with the SAME tenant_id=X
// override and it resolves to the receipt Search created. Both Search's
// recordRecallReceipt and ReportRecallOutcome bind via the identical
// resolveTenant(ctx, overrideID) path, so they always agree — an admin's
// receipt is bound to the override target, never their own home tenant.
func TestReportRecallOutcome_AdminOverrideFindsReceipt(t *testing.T) {
	f := newAuthzFixture(t)
	adminCtx := ctxFor(f.tenantA, f.admin)
	token := "admintok" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(adminCtx, "learnings", nil, "admin-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", &f.tenantB)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	_, recallID, err := f.svc.Search(adminCtx, token, nil, nil, 20, false, "", &f.tenantB)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, recallID, "admin override search must still issue a receipt")

	require.NoError(t, f.svc.ReportRecallOutcome(adminCtx, recallID, models.RecallOutcomeSuccess, &f.tenantB),
		"report with the SAME override must find the receipt Search created")

	hit, miss := recallOutcomeSectionCounts(t, f, secID)
	require.Equal(t, 1, hit)
	require.Equal(t, 0, miss)
}
