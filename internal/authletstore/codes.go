package authletstore

import (
	"context"
	"errors"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

// codeStore implements storage.CodeStore against GORM.
type codeStore struct{ db *gorm.DB }

// Save persists the authorization code, or storage.ErrAlreadyExists on a duplicate code_hash.
func (s *codeStore) Save(ctx context.Context, c storage.AuthCode) error {
	row := OAuthCode{
		CodeHash:      c.CodeHash,
		ClientID:      c.ClientID,
		UserID:        c.UserID,
		Resource:      c.Resource,
		Scope:         c.Scope,
		PKCEChallenge: c.PKCEChallenge,
		PKCEMethod:    c.PKCEMethod,
		RedirectURI:   c.RedirectURI,
		Nonce:         c.Nonce,
		ExpiresAt:     c.ExpiresAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return storage.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// ConsumeOnce atomically selects and deletes by code_hash in a tx, matching
// memstore semantics:
//   - (*AuthCode, nil) — code existed, not expired; now deleted.
//   - (nil, ErrNotFound) — code did not exist.
//   - (nil, ErrAlreadyConsumed) — code existed but expired; row deleted anyway.
func (s *codeStore) ConsumeOnce(ctx context.Context, hash string) (*storage.AuthCode, error) {
	var out *storage.AuthCode
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row OAuthCode
		if err := tx.Where("code_hash = ?", hash).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.ErrNotFound
			}
			return err
		}
		// The DELETE is the atomic guard (read only fetches the payload): under
		// Postgres READ COMMITTED both concurrent redemptions read the row, but the
		// row lock serializes DELETEs so only one wins — a code yields exactly one grant.
		res := tx.Where("code_hash = ?", hash).Delete(&OAuthCode{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return storage.ErrNotFound
		}
		if time.Now().After(row.ExpiresAt) {
			return storage.ErrAlreadyConsumed
		}
		out = &storage.AuthCode{
			CodeHash:      row.CodeHash,
			ClientID:      row.ClientID,
			UserID:        row.UserID,
			Resource:      row.Resource,
			Scope:         row.Scope,
			PKCEChallenge: row.PKCEChallenge,
			PKCEMethod:    row.PKCEMethod,
			RedirectURI:   row.RedirectURI,
			Nonce:         row.Nonce,
			ExpiresAt:     row.ExpiresAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteExpired removes codes with expires_at before the given instant; returns the count deleted.
func (s *codeStore) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	res := s.db.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&OAuthCode{})
	return int(res.RowsAffected), res.Error
}
