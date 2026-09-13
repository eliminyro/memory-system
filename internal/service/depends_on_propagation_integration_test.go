//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// docReview reads a document's persisted review-pending flag directly.
func docReview(t *testing.T, f *authzFixture, id uuid.UUID) (*time.Time, *string) {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, id).Error)
	return d.ReviewPendingAt, d.ReviewReason
}

// docReviewErr is the non-failing variant for polled goroutines: it returns the
// query error instead of require-ing, so a transient/teardown DB error retries
// rather than failing the test from inside Eventually/Never.
func docReviewErr(f *authzFixture, id uuid.UUID) (*time.Time, *string, error) {
	var d models.Document
	if err := f.db.First(&d, id).Error; err != nil {
		return nil, nil, err
	}
	return d.ReviewPendingAt, d.ReviewReason, nil
}

// TestDependsOn_ContentChangeFlagsDependent covers the whole depends_on lifecycle:
// a target content change flags the dependent (advisory, served in full), a no-op /
// heading-only / verify-only touch flags nothing, and re-verify clears the flag.
func TestDependsOn_ContentChangeFlagsDependent(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	dep, depSec := f.storeDoc(t, ctx, nil) // A: the dependent
	tgt, tgtSec := f.storeDoc(t, ctx, nil) // B: the dependency target

	_, err := f.svc.CreateEdge(ctx, dep, tgt, models.EdgeDependsOn, nil)
	require.NoError(t, err)

	tgtCat, tgtSlug := f.catSlug(t, tgt)
	tgtPath := tgtCat + "/" + tgtSlug

	// Changing B's content flags A (propagation is a detached side-effect: poll).
	body := "brand new dependency content"
	_, err = f.svc.UpdateSection(ctx, tgtSec, &body, nil, false, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		at, reason, err := docReviewErr(f, dep)
		if err != nil {
			return false
		}
		return at != nil && reason != nil && *reason == tgtPath
	}, 5*time.Second, 50*time.Millisecond, "dependent is flagged review-pending naming the target")

	// The flag is advisory: A reads back review-pending, reason, AND full content.
	view, err := f.svc.GetDocumentByID(ctx, dep, false, "", nil)
	require.NoError(t, err)
	require.True(t, view.ReviewPending, "read surfaces the review-pending signal")
	require.Equal(t, tgtPath, view.ReviewReason, "reason names the changed dependency")
	require.NotEmpty(t, view.Sections, "flagged doc is served in full")
	require.NotEmpty(t, view.Sections[0].Content, "content is not withheld or down-ranked")

	// Re-verifying A clears the flag (inline, observable to the next read).
	_, err = f.svc.UpdateSection(ctx, depSec, nil, nil, true, nil)
	require.NoError(t, err)
	at, _ := docReview(t, f, dep)
	require.Nil(t, at, "re-verify clears the review-pending flag")
	cleared, err := f.svc.GetDocumentByID(ctx, dep, false, "", nil)
	require.NoError(t, err)
	require.False(t, cleared.ReviewPending, "a subsequent read no longer reports it")

	// A heading-only edit of B flags nothing (no content change).
	newHeading := "Renamed heading"
	_, err = f.svc.UpdateSection(ctx, tgtSec, nil, &newHeading, false, nil)
	require.NoError(t, err)

	// A bare mark_verified of B flags nothing.
	require.NoError(t, f.svc.MarkVerified(ctx, tgtSec, nil))

	require.Never(t, func() bool {
		at, _, err := docReviewErr(f, dep)
		if err != nil {
			return false
		}
		return at != nil
	}, 500*time.Millisecond, 50*time.Millisecond, "no-op / verify-only touches never flag the dependent")
}
