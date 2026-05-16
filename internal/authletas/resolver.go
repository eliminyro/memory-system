// Package authletas wires authlet into memory-system: UserResolver and
// (in later phases) AS construction, JWKS/PRM handlers, and dual-auth
// middleware.
package authletas

import (
	"context"
	"errors"
	"log/slog"

	"github.com/eliminyro/authlet/pkg/idp"
	"gorm.io/gorm"
)

// ErrUnauthorized is returned when an upstream Google identity has no
// matching memory-system tenant. Memory MCP never auto-provisions tenants
// from federated claims — a row in tenant_users must already exist before
// MCP access is granted.
var ErrUnauthorized = errors.New("authletas: no tenant for google email")

// ResolverRejectEvent is the stable structured-log key emitted on every
// resolver rejection. Operators alert/aggregate on this value and read
// `reason` for the breakdown. Keeping it as a constant makes downstream
// log queries grep-stable across refactors.
const ResolverRejectEvent = "authletas.resolver.reject"

// Rejection reasons. These are the labels paired with the
// ResolverRejectEvent log entry and the values are the contract — do not
// rename without coordinating with whoever is aggregating on them.
const (
	rejectReasonEmailEmpty      = "email_empty"
	rejectReasonEmailUnverified = "email_unverified"
	rejectReasonTenantNotFound  = "tenant_not_found"
	rejectReasonDBError         = "db_error"
)

// MemoryUserResolver maps upstream Google OIDC claims to a memory-system
// tenant UUID by looking up the verified email address in the tenant_users
// table. Email is the trust anchor — we refuse to issue tokens for empty or
// unverified emails regardless of any DB state.
type MemoryUserResolver struct {
	DB *gorm.DB
	// Logger receives the structured rejection events. Nil falls back to
	// slog.Default so callers that wire one explicitly opt into a custom
	// destination (test capture buffers, distinct logger names, etc.).
	Logger *slog.Logger
}

// identityRow is the minimal projection the resolver needs. We deliberately
// avoid importing internal/models so authletas remains a leaf wiring package
// (stdlib + authlet + gorm only).
type identityRow struct {
	TenantID string `gorm:"column:tenant_id"`
}

func (identityRow) TableName() string { return "tenant_users" }

// Resolve looks up the memory-system tenant_id linked to the upstream Google
// email. It returns ErrUnauthorized for empty emails, for unverified emails
// (we never trust unverified claims — anyone with a Google account could
// otherwise claim any email), and for any DB miss or error (we don't leak
// DB internals as auth decisions). Callers (the authlet AS) treat all of
// these the same way: 403 at the /idp/callback redirect.
//
// Every rejection emits a single slog entry keyed event=ResolverRejectEvent
// with a `reason` label so operators can build a counter without changing
// the resolver. DB errors log at Warn (they may indicate infra trouble);
// the other three log at Info (expected hostile traffic).
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
	var row identityRow
	err := r.DB.WithContext(ctx).
		Where("email = ?", c.Email).
		Limit(1).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Info("authletas: resolver rejected",
				"event", ResolverRejectEvent,
				"reason", rejectReasonTenantNotFound,
				"email", c.Email)
		} else {
			logger.Warn("authletas: resolver rejected",
				"event", ResolverRejectEvent,
				"reason", rejectReasonDBError,
				"email", c.Email,
				"err", err)
		}
		return "", ErrUnauthorized
	}
	return row.TenantID, nil
}
