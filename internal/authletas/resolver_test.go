package authletas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/eliminyro/authlet/pkg/idp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// captureLogger returns a resolver-wired slog.Logger and the buffer the
// JSON handler writes into. Each line is one JSON record.
func captureLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// requireRejectEvent finds the first record matching reason and asserts the
// stable event key + level. Returns the parsed record for further checks.
func requireRejectEvent(t *testing.T, buf *bytes.Buffer, reason, wantLevel string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line not JSON: %v (raw=%q)", err, line)
		}
		if rec["event"] == ResolverRejectEvent && rec["reason"] == reason {
			if got := rec["level"]; got != wantLevel {
				t.Fatalf("level=%v want %v (rec=%v)", got, wantLevel, rec)
			}
			return rec
		}
	}
	t.Fatalf("no record with event=%s reason=%s found in: %s", ResolverRejectEvent, reason, buf.String())
	return nil
}

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

// Each rejection path must emit a single structured log record with the
// stable event key and the per-reason label so operators can build an
// aggregation/alert without further code changes.
func TestMemoryUserResolver_EmitsRejectEvent(t *testing.T) {
	t.Run("email_empty logs at INFO with no email field", func(t *testing.T) {
		logger, buf := captureLogger(t)
		db := openTestDB(t)
		r := &MemoryUserResolver{DB: db, Logger: logger}
		_, _ = r.Resolve(context.Background(), idp.Claims{Email: "", EmailVerified: true})
		rec := requireRejectEvent(t, buf, rejectReasonEmailEmpty, "INFO")
		if _, ok := rec["email"]; ok {
			t.Fatalf("email field must be absent for empty-email reject, rec=%v", rec)
		}
	})

	t.Run("email_unverified logs at INFO with email field", func(t *testing.T) {
		logger, buf := captureLogger(t)
		db := openTestDB(t)
		r := &MemoryUserResolver{DB: db, Logger: logger}
		_, _ = r.Resolve(context.Background(), idp.Claims{Email: "u@x", EmailVerified: false})
		rec := requireRejectEvent(t, buf, rejectReasonEmailUnverified, "INFO")
		if rec["email"] != "u@x" {
			t.Fatalf("email=%v want u@x", rec["email"])
		}
	})

	t.Run("tenant_not_found logs at INFO with email field", func(t *testing.T) {
		logger, buf := captureLogger(t)
		db := openTestDB(t)
		r := &MemoryUserResolver{DB: db, Logger: logger}
		_, _ = r.Resolve(context.Background(), idp.Claims{Email: "stranger@example", EmailVerified: true})
		rec := requireRejectEvent(t, buf, rejectReasonTenantNotFound, "INFO")
		if rec["email"] != "stranger@example" {
			t.Fatalf("email=%v want stranger@example", rec["email"])
		}
	})

	t.Run("happy path does not log a reject", func(t *testing.T) {
		logger, buf := captureLogger(t)
		db := openTestDB(t)
		seedTenantUser(t, db, "ok@x", "tenant-uuid-1", "admin")
		r := &MemoryUserResolver{DB: db, Logger: logger}
		if _, err := r.Resolve(context.Background(), idp.Claims{Email: "ok@x", EmailVerified: true}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), ResolverRejectEvent) {
			t.Fatalf("happy path must not emit reject event, got: %s", buf.String())
		}
	})
}

// TestMemoryUserResolver_NilLoggerFallsBackToDefault asserts the resolver
// does not panic when constructed without a Logger — callers in setup.go
// always wire one, but the zero value must remain usable.
func TestMemoryUserResolver_NilLoggerFallsBackToDefault(t *testing.T) {
	db := openTestDB(t)
	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{Email: "", EmailVerified: true})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}
