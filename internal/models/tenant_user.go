package models

import (
	"time"

	"github.com/google/uuid"
)

// Tenant user role constants. Memory MCP currently distinguishes only two
// roles at the email -> tenant mapping layer: regular member vs admin.
const (
	TenantUserRoleMember = "member"
	TenantUserRoleAdmin  = "admin"
)

// ValidTenantUserRoles is the accepted set for TenantUser.Role.
var ValidTenantUserRoles = map[string]struct{}{
	TenantUserRoleMember: {},
	TenantUserRoleAdmin:  {},
}

// TenantUser maps a verified upstream Google email address to a memory-system
// tenant. The authlet AS consults this table during sign-in to translate an
// upstream OIDC identity into a tenant_id for the issued access token. Rows
// are populated manually (admin SQL backfill); we never auto-provision from
// federated claims.
type TenantUser struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	// Email is globally unique — a single email maps to exactly one tenant.
	// size 320 = RFC 5321 max (64 local + @ + 255 domain).
	Email    string    `gorm:"size:320;not null;uniqueIndex" json:"email"`
	TenantID uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Role     string    `gorm:"size:16;not null;default:'member'" json:"role"`

	CreatedAt time.Time `json:"created_at"`

	// Tenant belongsTo relationship — drives the FK on tenant_id with
	// ON DELETE CASCADE so removing a tenant cleans up its email mappings.
	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TenantUser) TableName() string {
	return "tenant_users"
}
