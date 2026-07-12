package authletas

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openLookupTestDB returns an in-memory sqlite DB with a tenant_users table
// mirroring the columns lookupTenantEmail reads (no internal/models import).
func openLookupTestDB(t *testing.T) *gorm.DB {
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

func seedLookupRow(t *testing.T, db *gorm.DB, email, tenantID string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO tenant_users (email, tenant_id, role) VALUES (?, ?, 'member')",
		email, tenantID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// TestLookupTenantEmail_HitReturnsEmail: a row exists for tenant_id, helper
// returns its email.
func TestLookupTenantEmail_HitReturnsEmail(t *testing.T) {
	db := openLookupTestDB(t)
	seedLookupRow(t, db, "u@example.com", "tenant-uuid-1")

	got := lookupTenantEmail(context.Background(), db, slog.Default(), "tenant-uuid-1")
	if got != "u@example.com" {
		t.Fatalf("got %q, want u@example.com", got)
	}
}

// TestLookupTenantEmail_MissReturnsEmpty: no-row case returns "" (so
// additionalClaims returns nil and idTokenClaims returns zero values).
func TestLookupTenantEmail_MissReturnsEmpty(t *testing.T) {
	db := openLookupTestDB(t)
	// No seed — the table is empty.

	got := lookupTenantEmail(context.Background(), db, slog.Default(), "no-such-tenant")
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

// TestLookupTenantEmail_DBErrorReturnsEmptyAndLogs: a DB failure returns ""
// plus a warn log, never a panic.
func TestLookupTenantEmail_DBErrorReturnsEmptyAndLogs(t *testing.T) {
	db := openLookupTestDB(t)
	// Force a query error by dropping the table after migration.
	if err := db.Migrator().DropTable("tenant_users"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	got := lookupTenantEmail(context.Background(), db, logger, "any-tenant")
	if got != "" {
		t.Fatalf("got %q, want empty string on DB error", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("tenant_user email lookup failed")) {
		t.Fatalf("expected warn log on DB error, got: %q", buf.String())
	}
}
