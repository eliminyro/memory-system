//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestAPIKeyPurgeDeadBefore verifies the dead-key sweep query: keys revoked or
// expired before the cutoff are hard-deleted, while active keys and recently-dead
// keys (dead after the cutoff) survive. Assertions are by id so leftover rows from
// other tests can't skew the result.
func TestAPIKeyPurgeDeadBefore(t *testing.T) {
	db := openAPIKeyPG(t)
	repo := NewAPIKeyRepository(db)
	ctx := context.Background()

	twoAgo := time.Now().Add(-2 * time.Hour)
	nowish := time.Now()

	mk := func(label string, revoked, expires *time.Time) *models.APIKey {
		k := &models.APIKey{
			TenantID:  models.BootstrapTenantID,
			KeyHash:   uuid.NewString(),
			Label:     label,
			Prefix:    "mmcp_x",
			RevokedAt: revoked,
			ExpiresAt: expires,
		}
		if err := repo.Create(ctx, k); err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
		return k
	}

	active := mk("active", nil, nil)
	recentRevoked := mk("recent-revoked", &nowish, nil)
	oldRevoked := mk("old-revoked", &twoAgo, nil)
	oldExpired := mk("old-expired", nil, &twoAgo)

	n, err := repo.PurgeDeadBefore(ctx, time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("PurgeDeadBefore: %v", err)
	}
	if n < 2 {
		t.Fatalf("purged %d rows, want >= 2 (old revoked + old expired)", n)
	}

	// Dead-before-cutoff keys are gone.
	for _, k := range []*models.APIKey{oldRevoked, oldExpired} {
		if _, err := repo.GetByID(ctx, k.ID); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("%s: GetByID err = %v, want ErrNotFound (should be purged)", k.Label, err)
		}
	}
	// Active + recently-dead keys survive.
	for _, k := range []*models.APIKey{active, recentRevoked} {
		if _, err := repo.GetByID(ctx, k.ID); err != nil {
			t.Errorf("%s: GetByID err = %v, want survival", k.Label, err)
		}
	}
}
