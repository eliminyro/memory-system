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

// Save persists the authorization code. Returns storage.ErrAlreadyExists
// if a row with the same code_hash already exists.
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

// ConsumeOnce atomically selects and deletes by code_hash inside a tx.
// Matches memstore semantics:
//   - (*AuthCode, nil) — code existed, not expired; entry now deleted.
//   - (nil, ErrNotFound) — code did not exist.
//   - (nil, ErrAlreadyConsumed) — code existed but had expired; the row
//     is deleted as a side-effect.
func (s *codeStore) ConsumeOnce(ctx context.Context, hash string) (*storage.AuthCode, error) {
	var out *storage.AuthCode
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row OAuthCode
		if err := tx.Clauses().Where("code_hash = ?", hash).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.ErrNotFound
			}
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
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
			ExpiresAt:     row.ExpiresAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteExpired removes codes whose expires_at is before the supplied
// instant. Returns the number of rows deleted.
func (s *codeStore) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	res := s.db.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&OAuthCode{})
	return int(res.RowsAffected), res.Error
}
