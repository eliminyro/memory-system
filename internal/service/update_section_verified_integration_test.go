//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// sectionVerifiedAt reads a section's persisted verified_at directly.
func sectionVerifiedAt(t *testing.T, f *authzFixture, id uuid.UUID) *time.Time {
	t.Helper()
	var s models.Section
	require.NoError(t, f.db.First(&s, id).Error)
	return s.VerifiedAt
}

// TestUpdateSection_VerifiedStampsClock: update_section with verified=true stamps
// verified_at in the same call, while an update without the flag leaves it untouched.
func TestUpdateSection_VerifiedStampsClock(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	_, sec := f.storeDoc(t, ctx, nil)

	before := sectionVerifiedAt(t, f, sec)

	// Without the flag: content changes, verified_at does not.
	body1 := "revised content one"
	_, err := f.svc.UpdateSection(ctx, sec, &body1, nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, before, sectionVerifiedAt(t, f, sec), "update without verified leaves the clock untouched")

	// With the flag: verified_at is stamped and returned.
	body2 := "revised content two"
	updated, err := f.svc.UpdateSection(ctx, sec, &body2, nil, true, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.VerifiedAt, "returned section carries the new verified_at")

	after := sectionVerifiedAt(t, f, sec)
	require.NotNil(t, after, "verified_at is persisted")
	require.WithinDuration(t, time.Now(), *after, 30*time.Second, "verified_at is freshly stamped")
	if before != nil {
		require.True(t, after.After(*before), "verified_at advanced past the prior value")
	}
}
