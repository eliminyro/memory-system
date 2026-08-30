package authletstore

import (
	"context"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

// signingKeyStore implements storage.SigningKeyStore against GORM.
type signingKeyStore struct{ db *gorm.DB }

// signingKeyActivateLock is a fixed advisory-lock key that serializes
// concurrent signing-key ACTIVATION across replicas, so the deactivate-all +
// insert-active sequence can't interleave into two active rows (READ COMMITTED
// alone doesn't serialize it). Transaction-scoped: auto-released at tx end.
const signingKeyActivateLock = 0x5347_4b45_4143_54 // arbitrary stable int64 ("SGKEACT")

func toStorageKey(r AuthletSigningKey) storage.SigningKey {
	return storage.SigningKey{
		ID:                  r.ID,
		Algorithm:           r.Algorithm,
		PublicPEM:           r.PublicPEM,
		PrivatePEMEncrypted: r.PrivatePEMEncrypted,
		IsActive:            r.IsActive,
		CreatedAt:           r.CreatedAt,
		RetiresAt:           r.RetiresAt,
	}
}

// ListActive returns every key that has not yet retired (retires_at NULL or future).
func (s *signingKeyStore) ListActive(ctx context.Context) ([]storage.SigningKey, error) {
	var rows []AuthletSigningKey
	q := s.db.WithContext(ctx).Where("retires_at IS NULL OR retires_at > ?", time.Now().UTC())
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]storage.SigningKey, len(rows))
	for i, r := range rows {
		out[i] = toStorageKey(r)
	}
	return out, nil
}

// GetSigner returns the most recently created active key, or storage.ErrNotFound if none.
func (s *signingKeyStore) GetSigner(ctx context.Context) (storage.SigningKey, error) {
	var row AuthletSigningKey
	if err := s.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("created_at DESC").
		First(&row).Error; err != nil {
		return storage.SigningKey{}, mapGetErr(err)
	}
	return toStorageKey(row), nil
}

// Insert persists k. If k.IsActive, all prior active rows are deactivated in
// the same tx so exactly one row is active at a time.
func (s *signingKeyStore) Insert(ctx context.Context, k storage.SigningKey) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if k.IsActive {
			// Serialize activation across replicas so deactivate-all + insert-active
			// can't interleave into two active rows. Postgres-only: sqlite (the unit
			// tests) has no advisory locks and already serializes transactions on a
			// single connection, so the lock is unnecessary and absent there.
			if tx.Name() == "postgres" {
				if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(signingKeyActivateLock)).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&AuthletSigningKey{}).
				Where("is_active = ?", true).
				Update("is_active", false).Error; err != nil {
				return err
			}
		}
		row := AuthletSigningKey{
			ID:                  k.ID,
			Algorithm:           k.Algorithm,
			PublicPEM:           k.PublicPEM,
			PrivatePEMEncrypted: k.PrivatePEMEncrypted,
			IsActive:            k.IsActive,
			CreatedAt:           k.CreatedAt,
			RetiresAt:           k.RetiresAt,
		}
		return tx.Create(&row).Error
	})
}

// Retire marks the key inactive and sets retires_at, or storage.ErrNotFound if no row matches id.
func (s *signingKeyStore) Retire(ctx context.Context, id string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&AuthletSigningKey{}).
		Where("id = ?", id).
		Updates(map[string]any{"is_active": false, "retires_at": at})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return storage.ErrNotFound
	}
	return nil
}
