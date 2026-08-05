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

// captureLogger returns a JSON slog.Logger and the buffer it writes to (one
// JSON record per line).
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

// openTestDB returns an in-memory sqlite DB with just the tenant_users
// projection the resolver reads. Sqlite suffices — the only query is a simple
// `WHERE email = ?` lookup.
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
	// Private struct mirrors the tenant_users subset the resolver reads plus
	// the email/role columns tests populate.
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
	seedTenantUser(t, db, "admin@example.com", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "admin@example.com",
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

// TestMemoryUserResolver_UnverifiedEmailReturnsUnauthorized: email is seeded
// but EmailVerified=false must be rejected before the DB lookup, else an
// attacker could claim any email.
func TestMemoryUserResolver_UnverifiedEmailReturnsUnauthorized(t *testing.T) {
	db := openTestDB(t)
	seedTenantUser(t, db, "admin@example.com", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "admin@example.com",
		EmailVerified: false,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// TestMemoryUserResolver_EmptyEmailReturnsUnauthorized: the empty-email guard
// short-circuits before the DB lookup even when EmailVerified is true.
func TestMemoryUserResolver_EmptyEmailReturnsUnauthorized(t *testing.T) {
	db := openTestDB(t)
	// Seed a row to prove the guard short-circuits before the DB is consulted.
	seedTenantUser(t, db, "admin@example.com", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// Each rejection path emits one structured log record with the stable event
// key + per-reason label, so operators can aggregate without code changes.
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

// TestMemoryUserResolver_NilLoggerFallsBackToDefault: no Logger must not panic
// (zero value stays usable).
func TestMemoryUserResolver_NilLoggerFallsBackToDefault(t *testing.T) {
	db := openTestDB(t)
	r := &MemoryUserResolver{DB: db}
	_, err := r.Resolve(context.Background(), idp.Claims{Email: "", EmailVerified: true})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// TestMemoryUserResolver_NilProvisionRejectsUnknownEmail: with no Provision
// callback a tenant_users miss must preserve today's reject behavior.
func TestMemoryUserResolver_NilProvisionRejectsUnknownEmail(t *testing.T) {
	db := openTestDB(t)
	r := &MemoryUserResolver{DB: db} // Provision nil
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "stranger@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

// TestMemoryUserResolver_ProvisionResolvesTenant: a Provision callback that
// returns a tenant id on a miss makes Resolve return that id with no error.
func TestMemoryUserResolver_ProvisionResolvesTenant(t *testing.T) {
	db := openTestDB(t)
	called := false
	r := &MemoryUserResolver{
		DB: db,
		Provision: func(_ context.Context, c idp.Claims) (string, error) {
			called = true
			if c.Email != "new@example.com" {
				t.Fatalf("provision got email %q", c.Email)
			}
			return "tenant-provisioned-1", nil
		},
	}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "new@example.com",
		EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("Provision was not called on a tenant_users miss")
	}
	if id != "tenant-provisioned-1" {
		t.Fatalf("got %q, want tenant-provisioned-1", id)
	}
}

// TestMemoryUserResolver_ProvisionNotAllowedRejects: a Provision callback
// returning ErrProvisionNotAllowed yields ErrUnauthorized plus a reject event
// with reason "not_allowed" at INFO.
func TestMemoryUserResolver_ProvisionNotAllowedRejects(t *testing.T) {
	logger, buf := captureLogger(t)
	db := openTestDB(t)
	r := &MemoryUserResolver{
		DB:     db,
		Logger: logger,
		Provision: func(_ context.Context, _ idp.Claims) (string, error) {
			return "", ErrProvisionNotAllowed
		},
	}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "blocked@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	rec := requireRejectEvent(t, buf, rejectReasonNotAllowed, "INFO")
	if rec["email"] != "blocked@example.com" {
		t.Fatalf("email=%v want blocked@example.com", rec["email"])
	}
}

// TestMemoryUserResolver_ProvisionOtherErrorRejects: any non-sentinel provision
// error must not leak — Resolve returns ErrUnauthorized and logs a db_error
// reject at WARN.
func TestMemoryUserResolver_ProvisionOtherErrorRejects(t *testing.T) {
	logger, buf := captureLogger(t)
	db := openTestDB(t)
	r := &MemoryUserResolver{
		DB:     db,
		Logger: logger,
		Provision: func(_ context.Context, _ idp.Claims) (string, error) {
			return "", errors.New("provision backend exploded")
		},
	}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "boom@example.com",
		EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if id != "" {
		t.Fatalf("got id %q, want empty", id)
	}
	rec := requireRejectEvent(t, buf, rejectReasonDBError, "WARN")
	if rec["email"] != "boom@example.com" {
		t.Fatalf("email=%v want boom@example.com", rec["email"])
	}
	// The raw internal error text must not surface as the returned error.
	if strings.Contains(err.Error(), "exploded") {
		t.Fatalf("internal provision error leaked into returned error: %v", err)
	}
}
