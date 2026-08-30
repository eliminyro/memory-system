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

// openTestDB returns an in-memory sqlite DB mirroring the tenant_users columns
// the resolver reads: the (issuer, subject) identity anchor (composite unique)
// plus the unique email attribute. id is text (a uuid in production).
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
	type testTenantUserRow struct {
		ID       string  `gorm:"column:id;primaryKey"`
		Issuer   *string `gorm:"column:issuer;uniqueIndex:idx_tu_iss_sub"`
		Subject  *string `gorm:"column:subject;uniqueIndex:idx_tu_iss_sub"`
		Email    string  `gorm:"column:email;uniqueIndex"`
		TenantID string  `gorm:"column:tenant_id"`
		Role     string  `gorm:"column:role"`
	}
	if err := db.Table("tenant_users").AutoMigrate(&testTenantUserRow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedTenantUser inserts a subjectless (id, email, tenant_id, role) row via raw
// SQL — a legacy/admin-seeded mapping with NULL (issuer, subject).
func seedTenantUser(t *testing.T, db *gorm.DB, id, email, tenantID, role string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO tenant_users (id, email, tenant_id, role) VALUES (?, ?, ?, ?)",
		id, email, tenantID, role,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// seedTenantUserIdentity inserts a mapping already anchored to (issuer, subject).
func seedTenantUserIdentity(t *testing.T, db *gorm.DB, id, issuer, subject, email, tenantID, role string) {
	t.Helper()
	if err := db.Exec(
		"INSERT INTO tenant_users (id, issuer, subject, email, tenant_id, role) VALUES (?, ?, ?, ?, ?, ?)",
		id, issuer, subject, email, tenantID, role,
	).Error; err != nil {
		t.Fatal(err)
	}
}

// rowIdentity reads back the (issuer, subject, email) a row currently holds.
func rowIdentity(t *testing.T, db *gorm.DB, id string) (issuer, subject *string, email string) {
	t.Helper()
	var row struct {
		Issuer  *string `gorm:"column:issuer"`
		Subject *string `gorm:"column:subject"`
		Email   string  `gorm:"column:email"`
	}
	if err := db.Table("tenant_users").Select("issuer", "subject", "email").
		Where("id = ?", id).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Issuer, row.Subject, row.Email
}

// countTenantUsers returns the total tenant_users row count (adoption must not
// create a duplicate).
func countTenantUsers(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Table("tenant_users").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemoryUserResolver_LooksUpByVerifiedEmail(t *testing.T) {
	db := openTestDB(t)
	seedTenantUser(t, db, "user-uuid-1", "admin@example.com", "tenant-uuid-1", "admin")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Email:         "admin@example.com",
		EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "user-uuid-1" {
		t.Fatalf("got %q, want user-uuid-1 (tenant_users.id, not tenant_id)", id)
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
	seedTenantUser(t, db, "user-uuid-1", "admin@example.com", "tenant-uuid-1", "admin")

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
	seedTenantUser(t, db, "user-uuid-1", "admin@example.com", "tenant-uuid-1", "admin")

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
		seedTenantUser(t, db, "user-uuid-1", "ok@x", "tenant-uuid-1", "admin")
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

// TestMemoryUserResolver_ProvisionResolvesTenant: a Provision callback creates
// the tenant_users row (and returns the new tenant_id); Resolve must re-look-up
// and return the per-user tenant_users.id, NOT the tenant_id Provision returned.
func TestMemoryUserResolver_ProvisionResolvesTenant(t *testing.T) {
	db := openTestDB(t)
	called := false
	const provisionedUserID = "user-provisioned-1"
	r := &MemoryUserResolver{
		DB: db,
		Provision: func(_ context.Context, c idp.Claims) (string, error) {
			called = true
			if c.Email != "new@example.com" {
				t.Fatalf("provision got email %q", c.Email)
			}
			// Provision creates the tenant_users row and returns the tenant_id.
			seedTenantUser(t, db, provisionedUserID, "new@example.com", "tenant-provisioned-1", "owner")
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
	if id != provisionedUserID {
		t.Fatalf("got %q, want %q (per-user id, not the provisioned tenant_id)", id, provisionedUserID)
	}
}

// TestMemoryUserResolver_MultiUserTenant_ReturnsPerUserID is the regression test
// for the shared-tenant privilege-escalation bug (B1): two members of ONE tenant
// must resolve to their OWN distinct tenant_users.id — never the tenant_id, and
// never a co-tenant's id. Fails against the old code (which returned tenant_id).
func TestMemoryUserResolver_MultiUserTenant_ReturnsPerUserID(t *testing.T) {
	db := openTestDB(t)
	const tenantID = "tenant-shared-1"
	const idA = "user-a-id"
	const idB = "user-b-id"
	seedTenantUser(t, db, idA, "a@x", tenantID, "admin")
	seedTenantUser(t, db, idB, "b@x", tenantID, "member")

	r := &MemoryUserResolver{DB: db}
	got, err := r.Resolve(context.Background(), idp.Claims{Email: "b@x", EmailVerified: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == tenantID {
		t.Fatalf("resolved to tenant_id %q — the escalation bug is present", got)
	}
	if got == idA {
		t.Fatalf("resolved to co-tenant admin id %q — the escalation bug is present", got)
	}
	if got != idB {
		t.Fatalf("got %q, want member id %q", got, idB)
	}

	// The claim lookup feeding additionalClaims must key on the unique id and
	// return B's OWN email plus the shared tenant_id — never A's arbitrary email.
	email, tid, found := lookupUserClaims(context.Background(), db, slog.Default(), idB)
	if !found {
		t.Fatal("lookupUserClaims did not find seeded user B")
	}
	if email != "b@x" || tid != tenantID {
		t.Fatalf("claims = (%q, %q), want (b@x, %s)", email, tid, tenantID)
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

// TestMemoryUserResolver_ResolvesBySubject: a mapping anchored to (issuer,
// subject) resolves by that pair even when the claim's email has changed, and
// the row's email is refreshed to the new value.
func TestMemoryUserResolver_ResolvesBySubject(t *testing.T) {
	db := openTestDB(t)
	seedTenantUserIdentity(t, db, "user-1", "https://accounts.google.com", "sub-1", "old@example.com", "tenant-1", "owner")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "https://accounts.google.com", Subject: "sub-1",
		Email: "new@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "user-1" {
		t.Fatalf("got %q, want user-1 (resolved by subject)", id)
	}
	if _, _, email := rowIdentity(t, db, "user-1"); email != "new@example.com" {
		t.Fatalf("email = %q, want new@example.com (refreshed on login)", email)
	}
}

// TestMemoryUserResolver_AdoptsSubjectlessEmailRow: a subjectless email-only row
// (legacy/admin seed) is adopted on first login — (issuer, subject) stamped onto
// it, resolved to it, and NO duplicate row created.
func TestMemoryUserResolver_AdoptsSubjectlessEmailRow(t *testing.T) {
	db := openTestDB(t)
	seedTenantUser(t, db, "user-seed", "seed@example.com", "tenant-1", "owner")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "https://accounts.google.com", Subject: "sub-adopt",
		Email: "seed@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "user-seed" {
		t.Fatalf("got %q, want user-seed (adopted in place)", id)
	}
	if n := countTenantUsers(t, db); n != 1 {
		t.Fatalf("row count = %d, want 1 (adoption must not duplicate)", n)
	}
	iss, sub, _ := rowIdentity(t, db, "user-seed")
	if iss == nil || sub == nil || *iss != "https://accounts.google.com" || *sub != "sub-adopt" {
		t.Fatalf("identity = (%v, %v), want the stamped issuer/subject", iss, sub)
	}
	// A second login now resolves by the stamped subject.
	id2, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "https://accounts.google.com", Subject: "sub-adopt",
		Email: "seed@example.com", EmailVerified: true,
	})
	if err != nil || id2 != "user-seed" {
		t.Fatalf("second login: id=%q err=%v, want user-seed", id2, err)
	}
}

// TestMemoryUserResolver_MismatchedSubjectEmailFailsClosed: a verified email that
// matches a row anchored to a DIFFERENT (issuer, subject) must fail closed —
// a reused/reassigned address never authenticates as (or re-provisions over) it.
func TestMemoryUserResolver_MismatchedSubjectEmailFailsClosed(t *testing.T) {
	db := openTestDB(t)
	seedTenantUserIdentity(t, db, "user-A", "https://accounts.google.com", "sub-A", "alice@corp.com", "tenant-A", "owner")

	provisionCalled := false
	r := &MemoryUserResolver{DB: db, Provision: func(context.Context, idp.Claims) (string, error) {
		provisionCalled = true
		return "", nil
	}}
	_, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "https://accounts.google.com", Subject: "sub-B",
		Email: "alice@corp.com", EmailVerified: true,
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized (must not authenticate as sub-A)", err)
	}
	if provisionCalled {
		t.Fatal("must fail closed, not provision a new tenant over a mismatched email")
	}
	if iss, sub, _ := rowIdentity(t, db, "user-A"); iss == nil || sub == nil || *sub != "sub-A" {
		t.Fatalf("row A identity = (%v, %v), want it unchanged as sub-A", iss, sub)
	}
}

// TestMemoryUserResolver_EmailRefreshConflictKeepsOldEmail: when the claim email
// is already held by a DIFFERENT identity, the refresh is skipped (unique
// conflict), the old email kept, and the login still succeeds.
func TestMemoryUserResolver_EmailRefreshConflictKeepsOldEmail(t *testing.T) {
	db := openTestDB(t)
	seedTenantUserIdentity(t, db, "user-1", "iss", "sub-1", "a@example.com", "tenant-1", "owner")
	seedTenantUserIdentity(t, db, "user-2", "iss", "sub-2", "b@example.com", "tenant-2", "owner")

	r := &MemoryUserResolver{DB: db}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "iss", Subject: "sub-1",
		Email: "b@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("login must not fail on an email conflict: %v", err)
	}
	if id != "user-1" {
		t.Fatalf("got %q, want user-1", id)
	}
	if _, _, email := rowIdentity(t, db, "user-1"); email != "a@example.com" {
		t.Fatalf("email = %q, want a@example.com kept (refresh skipped on conflict)", email)
	}
}

// TestMemoryUserResolver_BothAnchorsMissProvisions: with an identity present,
// a miss on BOTH (issuer, subject) and email calls Provision, then resolves the
// per-user id of the newly provisioned row.
func TestMemoryUserResolver_BothAnchorsMissProvisions(t *testing.T) {
	db := openTestDB(t)
	called := false
	r := &MemoryUserResolver{
		DB: db,
		Provision: func(_ context.Context, c idp.Claims) (string, error) {
			called = true
			seedTenantUserIdentity(t, db, "user-prov", c.Issuer, c.Subject, c.Email, "tenant-prov", "owner")
			return "tenant-prov", nil
		},
	}
	id, err := r.Resolve(context.Background(), idp.Claims{
		Issuer: "iss", Subject: "sub-new",
		Email: "fresh@example.com", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("Provision was not called on a both-anchor miss")
	}
	if id != "user-prov" {
		t.Fatalf("got %q, want user-prov (per-user id, not tenant_id)", id)
	}
}
