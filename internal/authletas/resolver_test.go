package authletas

import (
	"context"
	"errors"
	"testing"

	"github.com/eliminyro/authlet/pkg/idp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openTestDB returns a fresh in-memory sqlite DB migrated with just the
// tenant_users projection the resolver reads. Sqlite is fine for the
// resolver because the only query it runs is a simple `WHERE email = ?`
// lookup — no postgres-specific features in play.
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
	// Sqlite :memory: connections do not share state across connections.
	sqlDB.SetMaxOpenConns(1)
	// AutoMigrate against a private struct that mirrors the subset of
	// tenant_users the resolver reads + the email/role columns tests need
	// to populate.
	type testTenantUserRow struct {
		ID       int64  `gorm:"primaryKey;autoIncrement"`
		Email    string `gorm:"column:email"`
		TenantID string `gorm:"column:tenant_id"`
		Role     string `gorm:"column:role"`
	}
	if err := db.Table("tenant_users").AutoMigrate(&testTenantUserRow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedTenantUser inserts an (email, tenant_id, role) row via raw SQL so the
// test does not depend on internal/models.
func seedTenantUser(t *testing.T, db *gorm.DB, email, tenantID, role string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO tenant_users (email, tenant_id, role) VALUES (?, ?, ?)",
		email, tenantID, role,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func TestMemoryUserResolver_LooksUpByVerifiedEmail(t *testing.T) {
	db := openTestDB(t)
	seedTenantUser(t, db, "pe@avantistudios.ai", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "pe@avantistudios.ai",
		EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "tenant-uuid-1" {
		t.Fatalf("got %q, want tenant-uuid-1", id)
	}
}

func TestMemoryUserResolver_UnknownEmailReturnsUnauthorized(t *testing.T) {
	db := openTestDB(t)
	r := &MemoryUserResolver{DB: db}

	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "stranger@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// TestMemoryUserResolver_UnverifiedEmailReturnsUnauthorized seeds the email
// in tenant_users, but the claim has EmailVerified=false. The resolver must
// short-circuit before hitting the DB — otherwise an attacker could claim
// any email on an account they don't own.
func TestMemoryUserResolver_UnverifiedEmailReturnsUnauthorized(t *testing.T) {
	db := openTestDB(t)
	seedTenantUser(t, db, "pe@avantistudios.ai", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "pe@avantistudios.ai",
		EmailVerified: false,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// TestMemoryUserResolver_EmptyEmailReturnsUnauthorized verifies the empty-
// email guard short-circuits before the DB lookup even when EmailVerified
// is somehow true.
func TestMemoryUserResolver_EmptyEmailReturnsUnauthorized(t *testing.T) {
	db := openTestDB(t)
	// Seed a row to prove the empty-email guard short-circuits before the
	// DB is consulted.
	seedTenantUser(t, db, "pe@avantistudios.ai", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}
