package authletstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/datatypes"
)

// uiClientExpiresAt is the far-future expiry stamped on the seeded UI client.
// Clients are reaped by last_used_at (see clientStore.DeleteExpired), not
// expires_at, so this value never triggers reaping; a fixed far-future stamp
// (rather than now()+N) keeps re-seeds byte-idempotent so the row is never
// rewritten just because time advanced.
var uiClientExpiresAt = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// uiClientMetadata returns the fixed OAuth metadata for the public PKCE UI
// client, matching the known-good hand-seeded row.
func uiClientMetadata() map[string]any {
	return map[string]any{
		"scope":                      "openid email profile",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
}

// uiRedirectURIs returns the single redirect URI the /ui SPA authenticates
// with. It MUST stay byte-identical to the redirect_uri served by
// internal/server.uiHandlers in /ui/config.json (baseURL + "/ui"): authlet
// validates redirect URIs by exact match, and the SPA sends cfg.redirect_uri on
// /authorize and /token. The construction is mirrored here so the two can never
// drift.
func uiRedirectURIs(baseURL string) []string {
	return []string{baseURL + "/ui"}
}

// SeedUIClient idempotently registers the public PKCE OAuth client the web UI
// authenticates as, so no operator ever hand-inserts an oauth_clients row. It
// runs at boot after the authlet tables are migrated, when OAuth is enabled.
//
// Behavior:
//   - absent: create a public (client_secret_hash NULL, token_endpoint_auth_method
//     "none") client with redirect_uris = [baseURL+"/ui"] and the fixed metadata.
//   - present and already our public/none shape: bring redirect_uris/metadata
//     back in sync with current config (so a changed PUBLIC_BASE_URL is picked
//     up), writing only when they actually differ.
//   - present but NOT our shape (a confidential client of the same id): leave it
//     untouched and log a warning — never clobber an operator's row.
//
// All persistence goes through the store's own helpers/models (GORM), never raw
// SQL.
func SeedUIClient(ctx context.Context, store *Store, clientID, baseURL string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if clientID == "" {
		return errors.New("authletstore: SeedUIClient requires a non-empty client id")
	}

	clients := store.Clients()
	redirects := uiRedirectURIs(baseURL)
	metadata := uiClientMetadata()

	existing, err := clients.Get(ctx, clientID)
	if errors.Is(err, storage.ErrNotFound) {
		now := time.Now().UTC()
		if err := clients.Create(ctx, storage.Client{
			ClientID:                clientID,
			SecretHash:              nil, // public client: client_secret_hash stays NULL
			TokenEndpointAuthMethod: "none",
			RedirectURIs:            redirects,
			Metadata:                metadata,
			CreatedAt:               now,
			LastUsedAt:              now,
			ExpiresAt:               uiClientExpiresAt,
		}); err != nil {
			return fmt.Errorf("authletstore: seed UI client: %w", err)
		}
		logger.Info("seeded UI OAuth client", "client_id", clientID, "redirect_uris", redirects)
		return nil
	}
	if err != nil {
		return fmt.Errorf("authletstore: look up UI client %q: %w", clientID, err)
	}

	// Guard: only manage a row that is our public/none shape. A confidential
	// client (secret set or non-"none" auth method) sharing the id is an
	// operator's — leave it untouched.
	if existing.TokenEndpointAuthMethod != "none" || len(existing.SecretHash) > 0 {
		logger.Warn("UI OAuth client id is registered as a non-public client; leaving it untouched",
			"client_id", clientID,
			"token_endpoint_auth_method", existing.TokenEndpointAuthMethod)
		return nil
	}

	// Ours and already in sync — nothing to write. Metadata is compared by
	// canonical JSON (encoding/json sorts map keys) so key/element-type
	// differences from the read round-trip don't register as drift.
	desiredMeta, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("authletstore: marshal UI client metadata: %w", err)
	}
	existingMeta, err := json.Marshal(existing.Metadata)
	if err != nil {
		return fmt.Errorf("authletstore: marshal existing UI client metadata: %w", err)
	}
	if slices.Equal(existing.RedirectURIs, redirects) && bytes.Equal(desiredMeta, existingMeta) {
		return nil
	}

	// Ours but drifted (e.g. PUBLIC_BASE_URL changed): resync redirect/metadata
	// via the ORM (not raw SQL).
	if err := store.db.WithContext(ctx).
		Model(&OAuthClient{}).
		Where("client_id = ?", clientID).
		Updates(map[string]any{
			"redirect_uris": StringArray(redirects),
			"metadata":      datatypes.JSON(desiredMeta),
		}).Error; err != nil {
		return fmt.Errorf("authletstore: update UI client %q: %w", clientID, err)
	}
	logger.Info("updated UI OAuth client", "client_id", clientID, "redirect_uris", redirects)
	return nil
}
