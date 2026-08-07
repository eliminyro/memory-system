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
)

// MemoryUserResolver maps upstream Google OIDC claims to a tenant UUID via the
// verified email in tenant_users. Email is the trust anchor — empty or
// unverified emails are refused regardless of DB state.
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

// identityRow is the resolver's minimal projection: the tenant_users primary
// key (a uuid text in production), which becomes the JWT subject. Avoids
// importing internal/models so authletas stays a leaf wiring package.
type identityRow struct {
	ID string `gorm:"column:id"`
}

func (identityRow) TableName() string { return "tenant_users" }

// Resolve returns the per-user tenant_users.id (the unified authorization
// subject) for the upstream Google email — NOT the tenant_id, so that co-tenants
// in a shared tenant map to distinct subjects. It returns ErrUnauthorized for
// empty/unverified emails (never trust unverified claims) and for any DB miss or
// error (don't leak DB state as auth decisions); the AS maps all to a 403 at
// /idp/callback. Every rejection emits one slog entry (event=ResolverRejectEvent
// + `reason`): DB errors at Warn, the rest at Info.
func (r *MemoryUserResolver) Resolve(ctx context.Context, c idp.Claims) (string, error) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if c.Email == "" {
		logger.Info("authletas: resolver rejected",
			"event", ResolverRejectEvent,
			"reason", rejectReasonEmailEmpty)
		return "", ErrUnauthorized
	}
	if !c.EmailVerified {
		logger.Info("authletas: resolver rejected",
			"event", ResolverRejectEvent,
			"reason", rejectReasonEmailUnverified,
			"email", c.Email)
		return "", ErrUnauthorized
	}
	id, found, err := lookupUserIDByEmail(ctx, r.DB, c.Email)
	if err != nil {
		logger.Warn("authletas: resolver rejected",
			"event", ResolverRejectEvent,
			"reason", rejectReasonDBError,
			"email", c.Email,
			"err", err)
		return "", ErrUnauthorized
	}
	if found {
		return id, nil
	}
	// tenant_users miss.
	if r.Provision != nil {
		// Provision resolves+creates a tenant (returning its tenant_id); we
		// discard that value — the subject is the per-user tenant_users.id, which
		// we re-look-up by email once the new row exists.
		if _, perr := r.Provision(ctx, c); perr != nil {
			if errors.Is(perr, ErrProvisionNotAllowed) {
				logger.Info("authletas: resolver rejected",
					"event", ResolverRejectEvent,
					"reason", rejectReasonNotAllowed,
					"email", c.Email)
				return "", ErrUnauthorized
			}
			// Any other provision error is internal; never leak it as anything
			// but a 403. Log at Warn for operators.
			logger.Warn("authletas: resolver rejected",
				"event", ResolverRejectEvent,
				"reason", rejectReasonDBError,
				"email", c.Email,
				"err", perr)
			return "", ErrUnauthorized
		}
		// Provision succeeded: the new tenant_users row now exists. Re-look-up
		// the per-user id by email — never return a tenant_id as the subject.
		uid, ok, lerr := lookupUserIDByEmail(ctx, r.DB, c.Email)
		if lerr != nil || !ok {
			logger.Warn("authletas: resolver rejected",
				"event", ResolverRejectEvent,
				"reason", rejectReasonDBError,
				"email", c.Email,
				"err", lerr)
			return "", ErrUnauthorized
		}
		return uid, nil
	}
	logger.Info("authletas: resolver rejected",
		"event", ResolverRejectEvent,
		"reason", rejectReasonTenantNotFound,
		"email", c.Email)
	return "", ErrUnauthorized
}

// lookupUserIDByEmail returns the tenant_users.id for a verified email. found is
// false (with nil err) when no row matches; a non-nil err is a real DB failure
// (record-not-found is normalized to found=false). The id is unique per user, so
// there is no LIMIT-ordering ambiguity across a shared tenant's members.
func lookupUserIDByEmail(ctx context.Context, db *gorm.DB, email string) (string, bool, error) {
	var row identityRow
	err := db.WithContext(ctx).
		Where("email = ?", email).
		Limit(1).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return row.ID, true, nil
}
