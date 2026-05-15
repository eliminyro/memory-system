package authletstore

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openTestDB returns a fresh in-memory sqlite DB with all authlet tables
// migrated. Each test gets its own DB so tests are isolated. The
// connection pool is capped at one so concurrent goroutines (used by the
// ConsumeOnce atomic test) all share the same memory database — without
// this, each new connection would see a different empty :memory: DB.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&OAuthClient{},
		&OAuthCode{},
		&OAuthRefreshToken{},
		&FamilyRevocation{},
		&AuthletSigningKey{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}
