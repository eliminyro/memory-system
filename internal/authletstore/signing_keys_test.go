package authletstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

func TestSigningKeyStore_InsertActiveDeactivatesPrior(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k1", IsActive: true, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k2", IsActive: true, CreatedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SigningKeys().GetSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "k2" {
		t.Fatalf("expected k2 active, got %q", got.ID)
	}
}

func TestSigningKeyStore_ListActiveExcludesRetired(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k1", IsActive: true, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k2", IsActive: true, CreatedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SigningKeys().Retire(ctx, "k1", time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	keys, err := s.SigningKeys().ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != "k2" {
		t.Fatalf("expected only k2, got %+v", keys)
	}
}

// TestSigningKeyStore_InsertActiveKeepsExactlyOneActive proves the single-active
// invariant survives repeated activating inserts: after two sequential Inserts
// with IsActive, GetSigner returns the latest and exactly one row carries
// is_active. The cross-replica race is closed by the advisory lock in Insert
// (Postgres-only; skipped here on sqlite, which serializes txns anyway) — this
// asserts the sequential correctness the lock must not disturb.
func TestSigningKeyStore_InsertActiveKeepsExactlyOneActive(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k1", IsActive: true, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := s.SigningKeys().Insert(ctx, storage.SigningKey{ID: "k2", IsActive: true, CreatedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	got, err := s.SigningKeys().GetSigner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "k2" {
		t.Fatalf("GetSigner = %q, want k2 (latest activation)", got.ID)
	}

	var active int64
	if err := db.Model(&AuthletSigningKey{}).Where("is_active = ?", true).Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active key count = %d, want exactly 1 (single-active invariant)", active)
	}
}

func TestSigningKeyStore_GetSignerNoneActive(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	if _, err := s.SigningKeys().GetSigner(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when no key, got %v", err)
	}
}
