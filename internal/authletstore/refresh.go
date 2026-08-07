package authletstore

import (
	"context"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

// refreshStore implements storage.RefreshTokenStore against GORM.
type refreshStore struct{ db *gorm.DB }

// Save persists rt, or storage.ErrAlreadyExists on a duplicate token_hash.
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
		return mapCreateErr(err)
	}
	return nil
}

// Get returns the token by hash, storage.ErrNotFound if absent, or
// storage.ErrFamilyRevoked if its family was revoked.
func (s *refreshStore) Get(ctx context.Context, hash string) (*storage.RefreshToken, error) {
	var row OAuthRefreshToken
	if err := s.db.WithContext(ctx).First(&row, "token_hash = ?", hash).Error; err != nil {
		return nil, mapGetErr(err)
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

// MarkUsed atomically transitions the token from active to replaced-by-replacedBy.
// A second call returns storage.ErrAlreadyConsumed; the caller MUST then RevokeFamily.
func (s *refreshStore) MarkUsed(ctx context.Context, hash string, replacedBy string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The conditional UPDATE is the atomic guard: only a row with empty
		// replaced_by transitions. Under Postgres READ COMMITTED the row lock
		// serializes the UPDATEs so only one concurrent rotation wins (loser: RowsAffected==0).
		res := tx.Model(&OAuthRefreshToken{}).
			Where("token_hash = ? AND replaced_by = ?", hash, "").
			Update("replaced_by", replacedBy)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// Distinguish an unknown token from one already consumed so the
			// caller triggers the rotation-reuse revoke path on the latter.
			var row OAuthRefreshToken
			if err := tx.First(&row, "token_hash = ?", hash).Error; err != nil {
				return mapGetErr(err)
			}
			return storage.ErrAlreadyConsumed
		}
		return nil
	})
}

// RevokeFamily inserts a FamilyRevocation row; a duplicate (same family_id) is a no-op.
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

// IsFamilyRevoked reports whether a FamilyRevocation row exists for familyID.
func (s *refreshStore) IsFamilyRevoked(ctx context.Context, familyID string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&FamilyRevocation{}).
		Where("family_id = ?", familyID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeleteExpired removes refresh tokens with expires_at before the given instant; returns the count deleted.
func (s *refreshStore) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	res := s.db.WithContext(ctx).Where("expires_at < ?", before).Delete(&OAuthRefreshToken{})
	return int(res.RowsAffected), res.Error
}
