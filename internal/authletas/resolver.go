// Package authletas wires authlet into memory-system: UserResolver, AS
// construction, JWKS/PRM handlers, and dual-auth middleware.
package authletas

import (
	"context"
	"errors"
	"log/slog"

	"github.com/eliminyro/authlet/pkg/idp"
	"gorm.io/gorm"
)

// ErrUnauthorized is returned when an upstream Google identity has no matching
// tenant and no Provision callback resolves one. Without a Provision hook the
// resolver never auto-provisions from federated claims — a tenant_users row
// must already exist.
var ErrUnauthorized = errors.New("authletas: no tenant for google email")

// ErrProvisionNotAllowed is the sentinel a Provision callback returns when the
// verified claims are not permitted to auto-provision a tenant. The
// composition root translates the service's not-allowed error into this
// sentinel before it reaches Resolve, which then emits the standard reject
// (reason=not_allowed) and returns ErrUnauthorized (a 403, never a leaked
// internal error).
var ErrProvisionNotAllowed = errors.New("authletas: provision not allowed")

// ResolverRejectEvent is the stable structured-log event key emitted on every
// resolver rejection; operators aggregate on it and read `reason`.
const ResolverRejectEvent = "authletas.resolver.reject"

// Rejection reason labels paired with ResolverRejectEvent. The values are a
// contract — don't rename without coordinating with log aggregators.
const (
	rejectReasonEmailEmpty      = "email_empty"
	rejectReasonEmailUnverified = "email_unverified"
	rejectReasonTenantNotFound  = "tenant_not_found"
	rejectReasonDBError         = "db_error"
	rejectReasonNotAllowed      = "not_allowed"
	rejectReasonSubjectMismatch = "subject_mismatch"
)

// MemoryUserResolver maps upstream OIDC claims to the per-user tenant_users.id.
// Identity is anchored on the (issuer, subject) pair; email is a mutable
// attribute. Empty or unverified emails are refused regardless of DB state.
type MemoryUserResolver struct {
	DB *gorm.DB
	// Logger receives the rejection events; nil falls back to slog.Default.
	Logger *slog.Logger
	// Provision, when non-nil, is called on a tenant_users miss to
	// provision-and-resolve a tenant for the verified claims, returning the new
	// tenant id. nil ⇒ no auto-provision (preserve today's reject: a missing
	// tenant_users row yields ErrUnauthorized). The composition root sets this
	// post-Setup; it must translate the service's not-allowed error into
	// ErrProvisionNotAllowed so Resolve can reject with reason=not_allowed.
	Provision func(ctx context.Context, c idp.Claims) (string, error)
}

// identityRow is the resolver's minimal projection: the tenant_users id (JWT
// subject) plus the columns the resolution order reads. Avoids importing
// internal/models so authletas stays a leaf wiring package.
type identityRow struct {
	ID      string  `gorm:"column:id"`
	Email   string  `gorm:"column:email"`
	Issuer  *string `gorm:"column:issuer"`
	Subject *string `gorm:"column:subject"`
}

func (identityRow) TableName() string { return "tenant_users" }

// Resolve returns the per-user tenant_users.id (the unified authorization
// subject) for the upstream Google email — NOT the tenant_id, so that co-tenants
// in a shared tenant map to distinct subjects. It returns ErrUnauthorized for
// empty/unverified emails (never trust unverified claims) and for any DB miss or
// error (don't leak DB state as auth decisions); the AS maps all to a 403 at
// /idp/callback. Every rejection emits one Debug slog entry
// (event=ResolverRejectEvent + `reason`) — normal auth churn on a hot path.
func (r *MemoryUserResolver) Resolve(ctx context.Context, c idp.Claims) (string, error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if c.Email == "" {
		logger.Debug("authletas: resolver rejected",
			"event", ResolverRejectEvent,
			"reason", rejectReasonEmailEmpty)
		return "", ErrUnauthorized
	}
	if !c.EmailVerified {
		logger.Debug("authletas: resolver rejected",
			"event", ResolverRejectEvent,
			"reason", rejectReasonEmailUnverified,
			"email", c.Email)
		return "", ErrUnauthorized
	}

	hasIdentity := c.Issuer != "" && c.Subject != ""

	// 1. Resolve by the (issuer, subject) identity anchor; refresh the email.
	if hasIdentity {
		row, found, err := lookupBySubject(ctx, r.DB, c.Issuer, c.Subject)
		if err != nil {
			return "", r.rejectDBError(logger, c.Email, err)
		}
		if found {
			r.refreshEmail(ctx, logger, row, c.Email)
			return row.ID, nil
		}
	}

	// 2. Fall back to email: adopt a subjectless legacy/seed row, but fail closed
	// when the email is already anchored to a DIFFERENT identity — a reused or
	// reassigned address must never authenticate as another subject.
	id, found, mismatch, err := r.resolveEmailAnchored(ctx, logger, c)
	if err != nil {
		return "", r.rejectDBError(logger, c.Email, err)
	}
	if mismatch {
		return "", r.rejectReason(logger, c.Email, rejectReasonSubjectMismatch)
	}
	if found {
		return id, nil
	}

	// 3. Miss on both anchors — provision if a callback allows it.
	if r.Provision != nil {
		if _, perr := r.Provision(ctx, c); perr != nil {
			if errors.Is(perr, ErrProvisionNotAllowed) {
				logger.Debug("authletas: resolver rejected",
					"event", ResolverRejectEvent,
					"reason", rejectReasonNotAllowed,
					"email", c.Email)
				return "", ErrUnauthorized
			}
			// Any other provision error is internal; never leak it (403 only).
			return "", r.rejectDBError(logger, c.Email, perr)
		}
		// Provision succeeded: re-resolve our own owner row by email (never return a
		// tenant_id as the subject). A mismatch or miss here is a concurrent-race
		// anomaly — fail closed.
		id, found, mismatch, lerr := r.resolveEmailAnchored(ctx, logger, c)
		if lerr != nil {
			return "", r.rejectDBError(logger, c.Email, lerr)
		}
		if mismatch || !found {
			return "", r.rejectReason(logger, c.Email, rejectReasonSubjectMismatch)
		}
		return id, nil
	}
	logger.Debug("authletas: resolver rejected",
		"event", ResolverRejectEvent,
		"reason", rejectReasonTenantNotFound,
		"email", c.Email)
	return "", ErrUnauthorized
}

// rejectDBError logs a db_error rejection at Warn — an infra failure, louder than
// normal auth churn — without leaking the internal error to the caller (403).
func (r *MemoryUserResolver) rejectDBError(logger *slog.Logger, email string, err error) error {
	logger.Warn("authletas: resolver rejected",
		"event", ResolverRejectEvent,
		"reason", rejectReasonDBError,
		"email", email,
		"err", err)
	return ErrUnauthorized
}

// rejectReason logs a Debug-level rejection with the given reason and returns
// ErrUnauthorized (a 403). For non-DB rejections.
func (r *MemoryUserResolver) rejectReason(logger *slog.Logger, email, reason string) error {
	logger.Debug("authletas: resolver rejected",
		"event", ResolverRejectEvent,
		"reason", reason,
		"email", email)
	return ErrUnauthorized
}

// resolveEmailAnchored resolves the email-matched mapping: an anchored row is
// returned only when its (issuer, subject) matches the claim; a subjectless row
// is adopted. mismatch=true ⇒ the email is another identity's; caller fails closed.
func (r *MemoryUserResolver) resolveEmailAnchored(ctx context.Context, logger *slog.Logger, c idp.Claims) (id string, found, mismatch bool, err error) {
	row, ok, err := lookupByEmail(ctx, r.DB, c.Email)
	if err != nil || !ok {
		return "", false, false, err
	}
	hasIdentity := c.Issuer != "" && c.Subject != ""
	if row.Subject != nil && *row.Subject != "" {
		if hasIdentity && row.Issuer != nil && *row.Issuer == c.Issuer && *row.Subject == c.Subject {
			return row.ID, true, false, nil
		}
		return "", false, true, nil
	}
	if hasIdentity {
		r.stampSubject(ctx, logger, row.ID, c.Issuer, c.Subject)
	}
	return row.ID, true, false, nil
}

// lookupBySubject returns the mapping anchored to (issuer, subject). found is
// false (nil err) when none matches; record-not-found is normalized to a miss.
func lookupBySubject(ctx context.Context, db *gorm.DB, issuer, subject string) (*identityRow, bool, error) {
	var row identityRow
	err := db.WithContext(ctx).
		Where("issuer = ? AND subject = ?", issuer, subject).
		Limit(1).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &row, true, nil
}

// lookupByEmail returns the mapping for a verified email. Email is unique, so
// there is no LIMIT-ordering ambiguity across a shared tenant's members.
func lookupByEmail(ctx context.Context, db *gorm.DB, email string) (*identityRow, bool, error) {
	var row identityRow
	err := db.WithContext(ctx).
		Where("email = ?", email).
		Limit(1).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &row, true, nil
}

// stampSubject anchors a subjectless mapping to (issuer, subject) on first login
// (adoption). A unique-conflict race is logged, never fatal — the email match
// already resolved the login.
func (r *MemoryUserResolver) stampSubject(ctx context.Context, logger *slog.Logger, id, issuer, subject string) {
	if err := r.DB.WithContext(ctx).
		Table("tenant_users").
		Where("id = ? AND subject IS NULL", id).
		Updates(map[string]any{"issuer": issuer, "subject": subject}).Error; err != nil {
		logger.Warn("authletas: subject adoption skipped", "id", id, "err", err)
	}
}

// refreshEmail updates a resolved mapping's email from the claim when it changed,
// but only if the new email is free; a unique conflict is logged and the old
// email kept — never fails the login.
func (r *MemoryUserResolver) refreshEmail(ctx context.Context, logger *slog.Logger, row *identityRow, newEmail string) {
	if newEmail == "" || newEmail == row.Email {
		return
	}
	if err := r.DB.WithContext(ctx).
		Table("tenant_users").
		Where("id = ?", row.ID).
		Update("email", newEmail).Error; err != nil {
		logger.Warn("authletas: email refresh skipped", "id", row.ID, "err", err)
	}
}
