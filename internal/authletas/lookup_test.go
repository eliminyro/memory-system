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
// mirroring the columns lookupUserClaims reads (no internal/models import). id
// is a text column matching the production uuid the lookup keys on.
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
		ID       string `gorm:"column:id;primaryKey"`
		Email    string `gorm:"column:email"`
		TenantID string `gorm:"column:tenant_id"`
		Role     string `gorm:"column:role"`
	}
	if err := db.Table("tenant_users").AutoMigrate(&testTenantUserRow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedLookupRow(t *testing.T, db *gorm.DB, id, email, tenantID string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO tenant_users (id, email, tenant_id, role) VALUES (?, ?, ?, 'member')",
		id, email, tenantID,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// TestLookupUserClaims_HitReturnsClaims: a row exists for the id, helper returns
// its email and tenant_id.
func TestLookupUserClaims_HitReturnsClaims(t *testing.T) {
	db := openLookupTestDB(t)
	seedLookupRow(t, db, "user-1", "u@example.com", "tenant-uuid-1")

	email, tid, found := lookupUserClaims(context.Background(), db, slog.Default(), "user-1")
	if !found {
		t.Fatal("expected found=true for seeded id")
	}
	if email != "u@example.com" {
		t.Fatalf("email = %q, want u@example.com", email)
	}
	if tid != "tenant-uuid-1" {
		t.Fatalf("tenant_id = %q, want tenant-uuid-1", tid)
	}
}

// TestLookupUserClaims_MissReturnsNotFound: no-row case returns found=false (so
// additionalClaims returns nil and idTokenClaims returns zero values).
func TestLookupUserClaims_MissReturnsNotFound(t *testing.T) {
	db := openLookupTestDB(t)
	// No seed — the table is empty.

	email, tid, found := lookupUserClaims(context.Background(), db, slog.Default(), "no-such-id")
	if found {
		t.Fatal("expected found=false on miss")
	}
	if email != "" || tid != "" {
		t.Fatalf("got (%q, %q), want empty strings", email, tid)
	}
}

// TestLookupUserClaims_DBErrorReturnsNotFoundAndLogs: a DB failure returns
// found=false plus a warn log, never a panic.
func TestLookupUserClaims_DBErrorReturnsNotFoundAndLogs(t *testing.T) {
	db := openLookupTestDB(t)
	// Force a query error by dropping the table after migration.
	if err := db.Migrator().DropTable("tenant_users"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	email, _, found := lookupUserClaims(context.Background(), db, logger, "any-id")
	if found {
		t.Fatal("expected found=false on DB error")
	}
	if email != "" {
		t.Fatalf("got %q, want empty string on DB error", email)
	}
	if !bytes.Contains(buf.Bytes(), []byte("tenant_user email lookup failed")) {
		t.Fatalf("expected warn log on DB error, got: %q", buf.String())
	}
}
