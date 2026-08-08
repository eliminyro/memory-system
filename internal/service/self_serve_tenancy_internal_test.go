package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestSignupDomainAllowed exercises the pure signup gate across the empty
// (public), single-entry, multi-entry, and Google hosted-domain (`hd`) cases.
func TestSignupDomainAllowed(t *testing.T) {
	cases := []struct {
		name    string
		email   string
		hd      string
		allowed []string
		want    bool
	}{
		{"empty list permits any domain", "alice@anywhere.com", "", nil, true},
		{"empty slice permits any domain", "alice@anywhere.com", "", []string{}, true},
		{"single entry permits listed domain", "carol@example.com", "", []string{"example.com"}, true},
		{"single entry blocks non-listed domain", "bob@other.com", "", []string{"example.com"}, false},
		{"multi entry permits any listed", "dave@second.org", "", []string{"first.com", "second.org"}, true},
		{"multi entry blocks unlisted", "eve@third.net", "", []string{"first.com", "second.org"}, false},
		{"case-insensitive email domain", "frank@Example.COM", "", []string{"example.com"}, true},
		{"case-insensitive allow entry", "grace@example.com", "", []string{"EXAMPLE.COM"}, true},
		{"hosted domain matches when email domain does not", "user@gmail.com", "corp.com", []string{"corp.com"}, true},
		{"hosted domain blocked when not listed", "user@gmail.com", "other.com", []string{"corp.com"}, false},
		{"whitespace in allow entry tolerated", "h@example.com", "", []string{"  example.com  "}, true},
		{"malformed email with no at blocked under non-empty list", "notanemail", "", []string{"example.com"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := signupDomainAllowed(c.email, c.hd, c.allowed); got != c.want {
				t.Fatalf("signupDomainAllowed(%q,%q,%v) = %v, want %v", c.email, c.hd, c.allowed, got, c.want)
			}
		})
	}
}

// TestDeriveTenantName proves the display name derives from the email local-part
// and falls back when there is no usable local-part.
func TestDeriveTenantName(t *testing.T) {
	cases := []struct {
		email    string
		fallback string
		want     string
	}{
		{"ada@example.com", "admin", "ada"},
		{"ada.lovelace@sub.example.com", "admin", "ada.lovelace"},
		{"bareword", "admin", "bareword"},
		{"@nolocalpart.com", "admin", "admin"},
		{"", "admin", "admin"},
		{"  ada@example.com", "admin", "ada"},
	}
	for _, c := range cases {
		if got := deriveTenantName(c.email, c.fallback); got != c.want {
			t.Fatalf("deriveTenantName(%q,%q) = %q, want %q", c.email, c.fallback, got, c.want)
		}
	}
}

// TestIsUniqueViolation covers the string-matched detection used by the
// provisioning race path.
func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"postgres duplicate key", errors.New(`ERROR: duplicate key value violates unique constraint "tenants_name_key"`), true},
		{"sqlite unique", errors.New("UNIQUE constraint failed: tenant_users.email"), true},
		{"unrelated", errors.New("connection refused"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUniqueViolation(c.err); got != c.want {
				t.Fatalf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestProvisionCandidateName covers the deterministic collision-safe naming:
// bare base, domain-qualified, ascending numeric suffixes, and the no-domain
// fallback.
func TestProvisionCandidateName(t *testing.T) {
	cases := []struct {
		base    string
		domain  string
		attempt int
		want    string
	}{
		{"Ada", "b.com", 0, "Ada"},
		{"Ada", "b.com", 1, "Ada (b.com)"},
		{"Ada", "b.com", 2, "Ada (b.com) 2"},
		{"Ada", "b.com", 3, "Ada (b.com) 3"},
		{"John Smith", "corp.io", 1, "John Smith (corp.io)"},
		{"Ada", "", 0, "Ada"},
		{"Ada", "", 1, "Ada 2"},
		{"Ada", "", 2, "Ada 3"},
	}
	for _, c := range cases {
		if got := provisionCandidateName(c.base, c.domain, c.attempt); got != c.want {
			t.Fatalf("provisionCandidateName(%q,%q,%d) = %q, want %q", c.base, c.domain, c.attempt, got, c.want)
		}
	}
}

// TestEmailDomain covers the '@' split used to build domain-qualified names.
func TestEmailDomain(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"alice@b.com", "b.com"},
		{"alice@B.COM", "b.com"},
		{"alice@ b.com ", "b.com"},
		{"noat", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := emailDomain(c.email); got != c.want {
			t.Fatalf("emailDomain(%q) = %q, want %q", c.email, got, c.want)
		}
	}
}

// TestUniqueViolationClassification proves the provisioning loop can tell a
// tenants.name collision (disambiguate + retry) from a tenant_users.email
// collision (resolve existing) on both Postgres- and sqlite-shaped messages.
func TestUniqueViolationClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantName  bool
		wantEmail bool
	}{
		{"pg name", errors.New(`ERROR: duplicate key value violates unique constraint "idx_tenants_name"`), true, false},
		{"pg email", errors.New(`ERROR: duplicate key value violates unique constraint "idx_tenant_users_email"`), false, true},
		{"sqlite name", errors.New("UNIQUE constraint failed: tenants.name"), true, false},
		{"sqlite email", errors.New("UNIQUE constraint failed: tenant_users.email"), false, true},
		{"unrelated", errors.New("connection refused"), false, false},
		{"nil", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isNameUniqueViolation(c.err); got != c.wantName {
				t.Fatalf("isNameUniqueViolation = %v, want %v", got, c.wantName)
			}
			if got := isEmailUniqueViolation(c.err); got != c.wantEmail {
				t.Fatalf("isEmailUniqueViolation = %v, want %v", got, c.wantEmail)
			}
		})
	}
}

// TestFilterTenantAccess covers the pure server-side type + q filter: type
// narrowing, case-insensitive name substring, exact-UUID match, and the
// non-nil-empty-slice guarantee.
func TestFilterTenantAccess(t *testing.T) {
	idA := uuid.New()
	idB := uuid.New()
	idC := uuid.New()
	all := []TenantAccess{
		{Tenant: models.Tenant{ID: idA, Name: "Ada Personal", Type: models.TenantTypePersonal}, Relation: "admin"},
		{Tenant: models.Tenant{ID: idB, Name: "Acme Shared", Type: models.TenantTypeShared}, Relation: "manager"},
		{Tenant: models.Tenant{ID: idC, Name: "Beta Shared", Type: models.TenantTypeShared}, Relation: "manager"},
	}

	t.Run("type filter keeps only that type", func(t *testing.T) {
		got := filterTenantAccess(all, models.TenantTypeShared, "")
		if len(got) != 2 {
			t.Fatalf("shared filter got %d, want 2", len(got))
		}
		for _, ta := range got {
			if ta.Tenant.Type != models.TenantTypeShared {
				t.Fatalf("unexpected type %q in shared filter", ta.Tenant.Type)
			}
		}
	})

	t.Run("empty type keeps all", func(t *testing.T) {
		if got := filterTenantAccess(all, "", ""); len(got) != 3 {
			t.Fatalf("empty type got %d, want 3", len(got))
		}
	})

	t.Run("q narrows by case-insensitive name substring", func(t *testing.T) {
		got := filterTenantAccess(all, "", "acme")
		if len(got) != 1 || got[0].Tenant.ID != idB {
			t.Fatalf("name substring got %+v, want only Acme Shared", got)
		}
	})

	t.Run("q narrows by exact tenant UUID", func(t *testing.T) {
		got := filterTenantAccess(all, "", idC.String())
		if len(got) != 1 || got[0].Tenant.ID != idC {
			t.Fatalf("uuid filter got %+v, want only Beta Shared", got)
		}
	})

	t.Run("type and q combine", func(t *testing.T) {
		got := filterTenantAccess(all, models.TenantTypeShared, "beta")
		if len(got) != 1 || got[0].Tenant.ID != idC {
			t.Fatalf("type+q got %+v, want only Beta Shared", got)
		}
	})

	t.Run("no match returns non-nil empty slice", func(t *testing.T) {
		got := filterTenantAccess(all, "", "nomatch")
		if got == nil {
			t.Fatal("filter must return a non-nil slice")
		}
		if len(got) != 0 {
			t.Fatalf("got %d, want 0", len(got))
		}
	})
}
