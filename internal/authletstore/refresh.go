package authletstore

import (
	"context"
	"errors"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

// refreshStore implements storage.RefreshTokenStore against GORM.
type refreshStore struct{ db *gorm.DB }

// Save persists rt. Returns storage.ErrAlreadyExists if a row with the
// same token_hash already exists.
func (s *refreshStore) Save(ctx context.Context, rt storage.RefreshToken) error {
	row := OAuthRefreshToken{
		TokenHash:  rt.TokenHash,
		FamilyID:   rt.FamilyID,
		ClientID:   rt.ClientID,
		UserID:     rt.UserID,
		Resource:   rt.Resource,
		Scope:      rt.Scope,
		ReplacedBy: rt.ReplacedBy,
		ExpiresAt:  rt.ExpiresAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return storage.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// Get returns the refresh token by hash or storage.ErrNotFound. If the
// token's family has been revoked, returns storage.ErrFamilyRevoked.
func (s *refreshStore) Get(ctx context.Context, hash string) (*storage.RefreshToken, error) {
	var row OAuthRefreshToken
	if err := s.db.WithContext(ctx).First(&row, "token_hash = ?", hash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	revoked, err := s.IsFamilyRevoked(ctx, row.FamilyID)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, storage.ErrFamilyRevoked
	}
	return &storage.RefreshToken{
		TokenHash:  row.TokenHash,
		FamilyID:   row.FamilyID,
		ClientID:   row.ClientID,
		UserID:     row.UserID,
		Resource:   row.Resource,
		Scope:      row.Scope,
		ReplacedBy: row.ReplacedBy,
		ExpiresAt:  row.ExpiresAt,
	}, nil
}

// MarkUsed atomically transitions the token from "active" to "replaced by
// replacedBy". A second call for the same hash returns
// storage.ErrAlreadyConsumed; the caller MUST then call RevokeFamily.
func (s *refreshStore) MarkUsed(ctx context.Context, hash string, replacedBy string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row OAuthRefreshToken
		if err := tx.First(&row, "token_hash = ?", hash).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return storage.ErrNotFound
			}
			return err
		}
		if row.ReplacedBy != "" {
			return storage.ErrAlreadyConsumed
		}
		return tx.Model(&row).Update("replaced_by", replacedBy).Error
	})
}

// RevokeFamily inserts a FamilyRevocation row. A duplicate revocation
// (same family_id) is a no-op.
func (s *refreshStore) RevokeFamily(ctx context.Context, familyID string) error {
	err := s.db.WithContext(ctx).Create(&FamilyRevocation{
		FamilyID:  familyID,
		RevokedAt: time.Now().UTC(),
	}).Error
	if err != nil && isUniqueViolation(err) {
		return nil
	}
	return err
}

// IsFamilyRevoked returns true if a FamilyRevocation row exists for
// familyID.
func (s *refreshStore) IsFamilyRevoked(ctx context.Context, familyID string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&FamilyRevocation{}).
		Where("family_id = ?", familyID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeleteExpired removes refresh tokens whose expires_at is before the
// supplied instant. Returns the number of rows deleted.
func (s *refreshStore) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&OAuthRefreshToken{})
	return int(res.RowsAffected), res.Error
}
