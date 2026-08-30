//go:build integration

package authletstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

// TestSeedUIClient_Postgres exercises the boot-time UI-client seed against real
// Postgres (text[] redirect_uris, JSON metadata, upsert). Skips unless
// TEST_DATABASE_URL is set (see openTestPG), so it compiles locally but only
// runs in CI.
func TestSeedUIClient_Postgres(t *testing.T) {
	db := openTestPG(t)
	store := New(db)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const clientID = "memory-ui"
	base1 := "https://mem.example.org"

	// 1. Fresh seed creates a public/"none" client with redirect base+"/ui".
	if err := SeedUIClient(ctx, store, clientID, base1, logger); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	got, err := store.Clients().Get(ctx, clientID)
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}
	if got.TokenEndpointAuthMethod != "none" {
		t.Fatalf("want token_endpoint_auth_method=none, got %q", got.TokenEndpointAuthMethod)
	}
	if len(got.SecretHash) != 0 {
		t.Fatalf("want NULL client_secret_hash (public client), got %d bytes", len(got.SecretHash))
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != base1+"/ui" {
		t.Fatalf("want redirect %q, got %+v", base1+"/ui", got.RedirectURIs)
	}
	if got.Metadata["scope"] != "openid email profile" {
		t.Fatalf("want scope metadata, got %+v", got.Metadata)
	}

	// 2. Re-seed with a changed base URL updates the redirect in place.
	base2 := "https://memory.example.net"
	if err := SeedUIClient(ctx, store, clientID, base2, logger); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	got, err = store.Clients().Get(ctx, clientID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != base2+"/ui" {
		t.Fatalf("want updated redirect %q, got %+v", base2+"/ui", got.RedirectURIs)
	}

	// 3. Guard: a pre-existing confidential client of the same id is left as-is.
	const opID = "operator-client"
	if err := store.Clients().Create(ctx, storage.Client{
		ClientID:                opID,
		SecretHash:              []byte("bcrypt$hash$here"),
		TokenEndpointAuthMethod: "client_secret_basic",
		RedirectURIs:            []string{"https://operator/cb"},
		Metadata:                map[string]any{"client_name": "operator"},
		CreatedAt:               time.Now().UTC(),
		LastUsedAt:              time.Now().UTC(),
		ExpiresAt:               time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed operator client: %v", err)
	}
	if err := SeedUIClient(ctx, store, opID, base1, logger); err != nil {
		t.Fatalf("seed guard: %v", err)
	}
	got, err = store.Clients().Get(ctx, opID)
	if err != nil {
		t.Fatalf("get operator: %v", err)
	}
	if got.TokenEndpointAuthMethod != "client_secret_basic" {
		t.Fatalf("guard clobbered auth method: %q", got.TokenEndpointAuthMethod)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "https://operator/cb" {
		t.Fatalf("guard clobbered redirects: %+v", got.RedirectURIs)
	}
	if len(got.SecretHash) == 0 {
		t.Fatalf("guard clobbered secret hash: want non-empty, got empty")
	}
}
