package authletstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

func TestClientStore_CRUDAndExpiry(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	secret := []byte("bcrypt$hash$here")
	created := time.Now().UTC().Truncate(time.Second)
	if err := s.Clients().Create(ctx, storage.Client{
		ClientID:                "c1",
		SecretHash:              secret,
		TokenEndpointAuthMethod: "client_secret_basic",
		RedirectURIs:            []string{"https://x/cb"},
		Metadata:                map[string]any{"client_name": "X"},
		CreatedAt:               created,
		LastUsedAt:              created,
		ExpiresAt:               created.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Clients().Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "c1" || got.Metadata["client_name"] != "X" {
		t.Fatalf("got %+v", got)
	}
	if !bytes.Equal(got.SecretHash, secret) {
		t.Fatalf("SecretHash round-trip failed: got %x want %x", got.SecretHash, secret)
	}
	if got.TokenEndpointAuthMethod != "client_secret_basic" {
		t.Fatalf("TokenEndpointAuthMethod round-trip failed: got %q", got.TokenEndpointAuthMethod)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "https://x/cb" {
		t.Fatalf("redirect URIs round-trip failed: %+v", got.RedirectURIs)
	}

	// Duplicate insert.
	if err := s.Clients().Create(ctx, storage.Client{ClientID: "c1"}); !errors.Is(err, storage.ErrAlreadyExists) {
		t.Fatalf("expected duplicate, got %v", err)
	}

	// Touch missing.
	if err := s.Clients().Touch(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("touch missing: got %v", err)
	}

	// Touch updates LastUsedAt.
	prev := got.LastUsedAt
	time.Sleep(20 * time.Millisecond)
	if err := s.Clients().Touch(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Clients().Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastUsedAt.After(prev) {
		t.Fatalf("expected LastUsedAt > %v, got %v", prev, after.LastUsedAt)
	}
}

func TestClientStore_DefaultAuthMethod(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	if err := s.Clients().Create(ctx, storage.Client{
		ClientID:   "pub",
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Clients().Get(ctx, "pub")
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenEndpointAuthMethod != "none" {
		t.Fatalf("expected default 'none', got %q", got.TokenEndpointAuthMethod)
	}
}

func TestClientStore_DeleteExpired(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now()

	if err := s.Clients().Create(ctx, storage.Client{
		ClientID: "old", LastUsedAt: old, CreatedAt: old, ExpiresAt: old.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Clients().Create(ctx, storage.Client{
		ClientID: "new", LastUsedAt: fresh, CreatedAt: fresh, ExpiresAt: fresh.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.Clients().DeleteExpired(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}
	if _, err := s.Clients().Get(ctx, "old"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected old gone, got %v", err)
	}
	if _, err := s.Clients().Get(ctx, "new"); err != nil {
		t.Fatalf("expected new kept, got %v", err)
	}
}
