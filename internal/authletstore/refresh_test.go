package authletstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

func TestRefreshStore_SaveAndGet(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{
		TokenHash: "h0", FamilyID: "fam0", ClientID: "c", UserID: "u",
		Resource: "r", Scope: "s", ExpiresAt: exp,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RefreshTokens().Get(ctx, "h0")
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenHash != "h0" || got.FamilyID != "fam0" || got.ClientID != "c" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestRefreshStore_MarkUsedReuseDetection(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{
		TokenHash: "h1", FamilyID: "fam", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTokens().MarkUsed(ctx, "h1", "h2"); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTokens().MarkUsed(ctx, "h1", "h3"); !errors.Is(err, storage.ErrAlreadyConsumed) {
		t.Fatalf("expected ErrAlreadyConsumed on reuse, got %v", err)
	}
}

func TestRefreshStore_MarkUsedNotFound(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if err := s.RefreshTokens().MarkUsed(ctx, "ghost", "x"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestRefreshStore_RevokeFamilyBlocksGet(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{
		TokenHash: "h1", FamilyID: "fam", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTokens().RevokeFamily(ctx, "fam"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshTokens().Get(ctx, "h1"); !errors.Is(err, storage.ErrFamilyRevoked) {
		t.Fatalf("expected ErrFamilyRevoked, got %v", err)
	}
}

func TestRefreshStore_IsFamilyRevoked(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	ok, err := s.RefreshTokens().IsFamilyRevoked(ctx, "famX")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected false for unknown family")
	}

	if err := s.RefreshTokens().RevokeFamily(ctx, "famX"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.RefreshTokens().IsFamilyRevoked(ctx, "famX")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected true after revoke")
	}

	// Double-revoke is a no-op.
	if err := s.RefreshTokens().RevokeFamily(ctx, "famX"); err != nil {
		t.Fatalf("double revoke should be no-op, got %v", err)
	}
}

func TestRefreshStore_DeleteExpired(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(time.Hour)
	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{TokenHash: "old", FamilyID: "f1", ExpiresAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{TokenHash: "new", FamilyID: "f2", ExpiresAt: fresh}); err != nil {
		t.Fatal(err)
	}

	n, err := s.RefreshTokens().DeleteExpired(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}
	if _, err := s.RefreshTokens().Get(ctx, "old"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected old gone, got %v", err)
	}
}
