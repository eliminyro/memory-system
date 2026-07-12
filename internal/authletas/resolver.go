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
// tenant. We never auto-provision from federated claims — a tenant_users row
// must already exist.
var ErrUnauthorized = errors.New("authletas: no tenant for google email")

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
)

// MemoryUserResolver maps upstream Google OIDC claims to a tenant UUID via the
// verified email in tenant_users. Email is the trust anchor — empty or
// unverified emails are refused regardless of DB state.
type MemoryUserResolver struct {
	DB *gorm.DB
	// Logger receives the rejection events; nil falls back to slog.Default.
	Logger *slog.Logger
}

// identityRow is the resolver's minimal projection. Avoids importing
// internal/models so authletas stays a leaf wiring package.
type identityRow struct {
	TenantID string `gorm:"column:tenant_id"`
}

func (identityRow) TableName() string { return "tenant_users" }

// Resolve returns the tenant_id for the upstream Google email. It returns
// ErrUnauthorized for empty/unverified emails (never trust unverified claims)
// and for any DB miss or error (don't leak DB state as auth decisions); the AS
// maps all to a 403 at /idp/callback. Every rejection emits one slog entry
// (event=ResolverRejectEvent + `reason`): DB errors at Warn, the rest at Info.
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
