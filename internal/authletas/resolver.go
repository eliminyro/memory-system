// Package authletas wires authlet into memory-system: UserResolver and
// (in later phases) AS construction, JWKS/PRM handlers, and dual-auth
// middleware.
package authletas

import (
	"context"
	"errors"

	"github.com/eliminyro/authlet/pkg/idp"
	"gorm.io/gorm"
)

// ErrUnauthorized is returned when an upstream Google identity has no
// matching memory-system tenant. Memory MCP never auto-provisions tenants
// from federated claims — a row in tenant_users must already exist before
// MCP access is granted.
var ErrUnauthorized = errors.New("authletas: no tenant for google email")

// MemoryUserResolver maps upstream Google OIDC claims to a memory-system
// tenant UUID by looking up the verified email address in the tenant_users
// table. Email is the trust anchor — we refuse to issue tokens for empty or
// unverified emails regardless of any DB state.
type MemoryUserResolver struct {
	DB *gorm.DB
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
func (r *MemoryUserResolver) Resolve(ctx context.Context, c idp.Claims) (string, error) {
	if c.Email == "" {
		return "", ErrUnauthorized
	}
	if !c.EmailVerified {
		return "", ErrUnauthorized
	}
	var row identityRow
	err := r.DB.WithContext(ctx).
		Where("email = ?", c.Email).
		Limit(1).
		First(&row).Error
	if err != nil {
		return "", ErrUnauthorized
	}
	return row.TenantID, nil
}
