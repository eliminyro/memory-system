//go:build integration

package authletstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

// TestDeleteExpired_SparesSeededUIClient proves clientStore.DeleteExpired
// idle-reaps a DCR client whose last_used_at is old, but never the seeded public
// UI client. The UI client carries the far-future uiClientExpiresAt sentinel and
// is only re-seeded at boot, so reaping it by last_used_at would break every /ui
// login until the pod restarts; the sentinel exclusion spares only the seed.
func TestDeleteExpired_SparesSeededUIClient(t *testing.T) {
	s := New(openTestPG(t))
	clients := s.Clients()
	ctx := context.Background()

	old := time.Now().UTC().Add(-90 * 24 * time.Hour)

	// Seeded UI client: old last_used_at, far-future sentinel expiry.
	if err := clients.Create(ctx, storage.Client{
		ClientID:                "ui-client",
		TokenEndpointAuthMethod: "none",
		RedirectURIs:            []string{"https://example.test/ui"},
		CreatedAt:               old,
		LastUsedAt:              old,
		ExpiresAt:               uiClientExpiresAt,
	}); err != nil {
		t.Fatalf("create UI client: %v", err)
	}

	// DCR-style client: equally old last_used_at, but a realistic near-future expiry.
	if err := clients.Create(ctx, storage.Client{
		ClientID:                "dcr-client",
		TokenEndpointAuthMethod: "none",
		RedirectURIs:            []string{"https://example.test/cb"},
		CreatedAt:               old,
		LastUsedAt:              old,
		ExpiresAt:               time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create DCR client: %v", err)
	}

	n, err := clients.DeleteExpired(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteExpired removed %d clients, want 1 (only the DCR client)", n)
	}

	// The DCR client is reaped.
	if _, err := clients.Get(ctx, "dcr-client"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("DCR client should have been reaped, got err=%v", err)
	}
	// The seeded UI client survives regardless of its old last_used_at.
	if _, err := clients.Get(ctx, "ui-client"); err != nil {
		t.Fatalf("seeded UI client must survive reaping, got err=%v", err)
	}
}
