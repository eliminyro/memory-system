//go:build integration

package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// backdateCreated moves a doc's created_at into the past so its expiry lands in a
// known window; the sole expiry clock is creation (no access/verify reprieve).
func backdateCreated(t *testing.T, f *authzFixture, id uuid.UUID, days int) {
	t.Helper()
	require.NoError(t, f.db.Exec(
		`UPDATE documents SET created_at = NOW() - make_interval(days => ?) WHERE id = ?`,
		days, id).Error)
}

// TestExpiringSoon_ReadSignals covers PR 2b: a prunable doc's read carries
// expires_at/expires_in_days, a sibling within 7 days shows in type_expiring_soon,
// and a knowledge (non-prunable) doc carries neither signal.
func TestExpiringSoon_ReadSignals(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	// A prunable journal (migrated default: prunable, 30-day expiration).
	slugA := "j" + uuid.NewString()
	resA, err := f.svc.StoreDocument(ctx, "journal", nil, slugA, "## H\nfresh journal", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, models.DocTypeJournal, resA.Document.DocType, "journal category derives the journal doc_type")

	// A same-type sibling backdated so it expires in ~5 days (within the 7-day window).
	slugB := "j" + uuid.NewString()
	resB, err := f.svc.StoreDocument(ctx, "journal", nil, slugB, "## H\nsibling journal", false, "", nil, nil)
	require.NoError(t, err)
	backdateCreated(t, f, resB.Document.ID, 25)
	siblingPath := models.BuildPath("journal", nil, slugB)

	// Reading the prunable doc carries its own expiry and lists the expiring sibling.
	view, err := f.svc.GetDocument(ctx, "journal", nil, slugA, false, "", nil)
	require.NoError(t, err)
	require.NotNil(t, view.ExpiresAt, "prunable read carries expires_at")
	require.NotNil(t, view.ExpiresInDays, "prunable read carries expires_in_days")
	require.Greater(t, *view.ExpiresInDays, 0, "a fresh 30-day journal has days left")
	require.LessOrEqual(t, *view.ExpiresInDays, 30)

	var found bool
	var days int
	for _, e := range view.TypeExpiringSoon {
		if e.Path == siblingPath {
			found, days = true, e.ExpiresInDays
			break
		}
	}
	require.True(t, found, "the sibling expiring within 7 days is listed")
	require.Greater(t, days, 0)
	require.LessOrEqual(t, days, 7, "listed only when expiry is within 7 days")

	// A knowledge (non-prunable) doc carries neither expiry signal.
	catK, slugK := f.catSlug(t, f.docA)
	kview, err := f.svc.GetDocument(ctx, catK, nil, slugK, false, "", nil)
	require.NoError(t, err)
	require.Nil(t, kview.ExpiresAt, "non-prunable read carries no expires_at")
	require.Nil(t, kview.ExpiresInDays, "non-prunable read carries no expires_in_days")
	require.Empty(t, kview.TypeExpiringSoon, "non-prunable read surfaces no expiring-soon list")
}
