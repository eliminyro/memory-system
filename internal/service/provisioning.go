package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// maxNameAttempts bounds the collision-safe naming retry in
// ProvisionPersonalTenant: candidate names are the base name, then the
// domain-qualified form, then numeric suffixes. A verified email should always
// land well within this bound; exhausting it is treated as a server error.
const maxNameAttempts = 12

// ErrSignupNotAllowed is returned by ProvisionPersonalTenant when the signup
// domain gate blocks an email. The auth adapter maps it to HTTP 403; no tenant
// is created. Distinct from apperr.ErrInvalidInput so front-ends can tell a
// gate rejection apart from a malformed request.
var ErrSignupNotAllowed = errors.New("signup not allowed: email domain is not permitted")

// signupDomainAllowed is the pure signup gate (design D2). An empty allow-list
// permits any domain (public instance). Otherwise it permits the email only
// when the email's domain (the part after '@', lowercased) OR the Google hosted
// domain claim (when present) matches an allow-list entry, case-insensitively.
// All comparisons trim/lowercase so the helper is robust even if the caller did
// not pre-normalize the list.
func signupDomainAllowed(email, hostedDomain string, allowedDomains []string) bool {
	if len(allowedDomains) == 0 {
		return true
	}
	emailDomain := ""
	if at := strings.LastIndex(email, "@"); at >= 0 {
		emailDomain = strings.ToLower(strings.TrimSpace(email[at+1:]))
	}
	hd := strings.ToLower(strings.TrimSpace(hostedDomain))
	for _, d := range allowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if emailDomain != "" && d == emailDomain {
			return true
		}
		if hd != "" && d == hd {
			return true
		}
	}
	return false
}

// deriveTenantName produces a display name from an email's local-part (the part
// before '@'), falling back to fallback when the email yields no usable
// local-part. Used for auto-provisioned personal tenants and the bootstrap
// founding tenant's derived default (design D7).
func deriveTenantName(email, fallback string) string {
	local := email
	if at := strings.Index(email, "@"); at >= 0 {
		local = email[:at]
	}
	local = strings.TrimSpace(local)
	if local == "" {
		return fallback
	}
	return local
}

// isUniqueViolation reports whether err is a unique-constraint violation.
// String-matched so it works across Postgres ("duplicate key ...") and sqlite
// ("UNIQUE constraint failed"), mirroring internal/authletstore's helper.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate")
}

// emailDomain returns the lowercased/trimmed part after '@' in email, or "" when
// there is none.
func emailDomain(email string) string {
	if at := strings.LastIndex(email, "@"); at >= 0 {
		return strings.ToLower(strings.TrimSpace(email[at+1:]))
	}
	return ""
}

// isEmailUniqueViolation / isNameUniqueViolation classify a provisioning
// unique-constraint error. The only two unique constraints touched inside the
// provisioning transaction are tenant_users.email and tenants.name; the DB error
// text names the offending constraint/column, so an "email" mention marks the
// email collision (same email — resolve and return the existing tenant) while a
// "name" mention marks the display-name collision (a DIFFERENT email whose base
// name is already taken — disambiguate and retry). Works on both Postgres
// ("...unique constraint \"idx_tenant_users_email\"") and sqlite ("UNIQUE
// constraint failed: tenants.name").
func isEmailUniqueViolation(err error) bool {
	return isUniqueViolation(err) && strings.Contains(strings.ToLower(err.Error()), "email")
}

func isNameUniqueViolation(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	l := strings.ToLower(err.Error())
	return strings.Contains(l, "name") && !strings.Contains(l, "email")
}

// provisionCandidateName produces the attempt-th collision-safe tenant name from
// a base name and the email domain: attempt 0 is the bare base, attempt 1 is the
// domain-qualified form ("Ada (b.com)"), and later attempts append an ascending
// numeric suffix ("Ada (b.com) 2", ...). When no domain is available it falls
// back to a plain numeric suffix ("Ada 2", ...). Deterministic given the inputs.
func provisionCandidateName(base, domain string, attempt int) string {
	switch {
	case attempt == 0:
		return base
	case domain == "":
		return base + " " + strconv.Itoa(attempt+1)
	case attempt == 1:
		return base + " (" + domain + ")"
	default:
		return base + " (" + domain + ") " + strconv.Itoa(attempt)
	}
}

// ProvisionPersonalTenant auto-provisions a personal tenant for a verified email
// on first login (design D1-D3). It gate-checks the email domain against
// allowedDomains (honoring the Google `hd` hosted-domain claim when non-empty);
// on block it returns ErrSignupNotAllowed and creates nothing. On pass it runs,
// in ONE transaction, CreateTenant(type=personal, name=<base>) then
// GrantTenantUser(email, tenant, owner) — the same pair the bootstrap path uses,
// so the resolved subject (tenant_users.id) and tuples match a hand-provisioned
// owner (personal-owner-role). It returns the tenant id (UUID string).
//
// The name base is displayName when non-empty (the OIDC `name` claim, the
// product's primary source), else the email local-part.
//
// Returning user: if the email already maps to a tenant, that tenant id is
// returned immediately — no create, no disambiguation.
//
// Same-email race: two concurrent first-logins of the SAME email collide on the
// globally-unique tenant_users.email; the loser re-resolves and returns the
// winning tenant — never a second tenant.
//
// Distinct-email name collision: two different emails whose base name is equal
// (e.g. two "John Smith", or alice@a.com vs alice@b.com) must BOTH provision.
// tenants.name is globally unique, so on a Name unique-violation the name is
// disambiguated (domain-qualified, then numeric) and creation retried within a
// bounded loop, yielding distinct tenants.
//
// Auto-provisioned personal tenants get NO public-read wildcard (that is
// default-pool-only); they are private to their owner.
func (s *MemoryService) ProvisionPersonalTenant(ctx context.Context, email, displayName, hostedDomain string, allowedDomains []string, issuer, subject string) (string, error) {
	if email == "" {
		return "", fmt.Errorf("%w: email required for provisioning", apperr.ErrInvalidInput)
	}
	// Gate closed until the instance is bootstrapped: refuse auto-provision until a
	// founding admin exists (HasAnyAdmin). A pre-bootstrap first login would
	// otherwise create a tenant_users row that then collides with /bootstrap's
	// GrantTenantUser (duplicate email -> 500, wedged instance). Fail closed with
	// the same ErrSignupNotAllowed the domain gate uses (mapped to 403) and create
	// nothing; auto-provision resumes once an admin exists. This precedes the domain
	// gate and any DB write.
	if hasAdmin, err := s.HasAnyAdmin(ctx); err != nil {
		return "", err
	} else if !hasAdmin {
		return "", ErrSignupNotAllowed
	}
	if !signupDomainAllowed(email, hostedDomain, allowedDomains) {
		return "", ErrSignupNotAllowed
	}

	// Fast path: an already-provisioned email returns its existing tenant without
	// entering the create/disambiguate loop (and without a spurious name-collision
	// retry against its own tenant).
	if tu, err := s.resolveSubjectByEmail(ctx, email); err == nil {
		return tu.TenantID.String(), nil
	}

	base := strings.TrimSpace(displayName)
	if base == "" {
		base = deriveTenantName(email, email)
	}
	domain := emailDomain(email)

	for attempt := 0; attempt < maxNameAttempts; attempt++ {
		name := provisionCandidateName(base, domain, attempt)
		var tenantID string
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			txSvc := s.withTx(tx)
			// Pre-auth provisioning has no authenticated subject; run the admin-gated
			// lifecycle methods under a local-admin context, exactly like Bootstrap.
			adminCtx := auth.WithLocalAdmin(ctx)
			// CreateTenant provisions the owner tenant_users row for this personal
			// tenant; anchor it to the login's (issuer, subject) from the start (D4).
			tenant, err := txSvc.CreateTenant(adminCtx, name, email, models.TenantTypePersonal)
			if err != nil {
				return err
			}
			if err := txSvc.stampOwnerSubject(adminCtx, tenant.ID, issuer, subject); err != nil {
				return err
			}
			tenantID = tenant.ID.String()
			return nil
		})
		switch {
		case err == nil:
			return tenantID, nil
		case isEmailUniqueViolation(err):
			// A concurrent first-login of the same email won the race. Read after
			// the write and return the winning tenant — never a second one.
			if tu, lookupErr := s.resolveSubjectByEmail(ctx, email); lookupErr == nil {
				return tu.TenantID.String(), nil
			}
			return "", err
		case isNameUniqueViolation(err):
			// A different email already holds this name; try the next candidate.
			continue
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("provision personal tenant: could not derive a unique name for %s after %d attempts", email, maxNameAttempts)
}

// stampOwnerSubject anchors a freshly-created personal tenant's owner mapping to
// the login's (issuer, subject). A no-op when either is empty (nothing to anchor);
// the subject-IS-NULL guard avoids clobbering an already-anchored row.
func (s *MemoryService) stampOwnerSubject(ctx context.Context, tenantID uuid.UUID, issuer, subject string) error {
	if issuer == "" || subject == "" {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&models.TenantUser{}).
		Where("tenant_id = ? AND subject IS NULL", tenantID).
		Updates(map[string]any{"issuer": issuer, "subject": subject}).Error
}
