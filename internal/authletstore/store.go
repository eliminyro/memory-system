package authletstore

import (
	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

// Store is memory-system's storage.Storage implementation. It groups the four
// sub-stores against a single *gorm.DB so they share transactions and
// connection pooling.
type Store struct {
	db *gorm.DB
}

// New constructs a Store backed by the supplied GORM DB. The DB must
// already have the authlet tables migrated.
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Clients returns the ClientStore view.
func (s *Store) Clients() storage.ClientStore { return &clientStore{db: s.db} }

// Codes returns the CodeStore view.
func (s *Store) Codes() storage.CodeStore { return &codeStore{db: s.db} }

// RefreshTokens returns the RefreshTokenStore view.
func (s *Store) RefreshTokens() storage.RefreshTokenStore { return &refreshStore{db: s.db} }

// SigningKeys returns the SigningKeyStore view.
func (s *Store) SigningKeys() storage.SigningKeyStore { return &signingKeyStore{db: s.db} }

var _ storage.Storage = (*Store)(nil)
