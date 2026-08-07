package authletstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// clientStore implements storage.ClientStore against GORM.
type clientStore struct{ db *gorm.DB }

// Create persists c. SecretHash and TokenEndpointAuthMethod go in top-level
// columns so SQL-seeded confidential clients need no metadata unmarshalling.
// Empty TokenEndpointAuthMethod defaults to "none" (public, PKCE-only).
func (s *clientStore) Create(ctx context.Context, c storage.Client) error {
	meta, _ := json.Marshal(c.Metadata)
	method := c.TokenEndpointAuthMethod
	if method == "" {
		method = "none"
	}
	row := OAuthClient{
		ClientID:                c.ClientID,
		ClientSecretHash:        c.SecretHash,
		TokenEndpointAuthMethod: method,
		RedirectURIs:            StringArray(c.RedirectURIs),
		Metadata:                datatypes.JSON(meta),
		CreatedAt:               c.CreatedAt,
		LastUsedAt:              c.LastUsedAt,
		ExpiresAt:               c.ExpiresAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return mapCreateErr(err)
	}
	return nil
}

// Get returns the client by ID or storage.ErrNotFound. SecretHash and
// TokenEndpointAuthMethod are round-tripped from their dedicated columns.
func (s *clientStore) Get(ctx context.Context, id string) (*storage.Client, error) {
	var row OAuthClient
	if err := s.db.WithContext(ctx).First(&row, "client_id = ?", id).Error; err != nil {
		return nil, mapGetErr(err)
	}
	meta := map[string]any{}
	_ = json.Unmarshal(row.Metadata, &meta)
	return &storage.Client{
		ClientID:                row.ClientID,
		SecretHash:              row.ClientSecretHash,
		TokenEndpointAuthMethod: row.TokenEndpointAuthMethod,
		RedirectURIs:            []string(row.RedirectURIs),
		Metadata:                meta,
		CreatedAt:               row.CreatedAt,
		LastUsedAt:              row.LastUsedAt,
		ExpiresAt:               row.ExpiresAt,
	}, nil
}

// Touch sets last_used_at to now, or storage.ErrNotFound if no row matches.
func (s *clientStore) Touch(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Model(&OAuthClient{}).
		Where("client_id = ?", id).
		Update("last_used_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// DeleteExpired removes clients with last_used_at older than olderThan; returns the count deleted.
func (s *clientStore) DeleteExpired(ctx context.Context, olderThan time.Time) (int, error) {
	// Never idle-reap the seeded public UI client: it carries the far-future
	// uiClientExpiresAt sentinel (seed.go) and is only re-seeded at boot, so
	// reaping it by last_used_at would break every /ui login until the pod
	// restarts. DCR clients carry realistic expiries, so this spares only the seed.
	res := s.db.WithContext(ctx).
		Where("last_used_at < ? AND expires_at <> ?", olderThan, uiClientExpiresAt).
		Delete(&OAuthClient{})
	return int(res.RowsAffected), res.Error
}
