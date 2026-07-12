package authletstore

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openTestDB returns a fresh in-memory sqlite DB with the authlet tables
// migrated; each test gets its own for isolation. The pool is capped at one so
// concurrent goroutines share one :memory: DB (else each connection sees its own).
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
