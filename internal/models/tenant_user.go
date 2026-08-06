package models

import (
	"time"

	"github.com/google/uuid"
)

// Tenant user role constants at the email->tenant mapping layer. member/admin
// apply to any tenant; owner is personal-tenant only — a full self-manager of
// their own tenant (owner ⇒ manager) that is NOT a system admin.
const (
	TenantUserRoleMember = "member"
	TenantUserRoleAdmin  = "admin"
	TenantUserRoleOwner  = "owner"
)

// ValidTenantUserRoles is the accepted set for TenantUser.Role.
var ValidTenantUserRoles = map[string]struct{}{
	TenantUserRoleMember: {},
	TenantUserRoleAdmin:  {},
	TenantUserRoleOwner:  {},
}

// TenantUser maps a verified upstream Google email to a tenant. The authlet AS
// consults it at sign-in to translate an OIDC identity into a tenant_id. Rows are
// populated manually (admin SQL); never auto-provisioned from federated claims.
type TenantUser struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	// Email is globally unique — a single email maps to exactly one tenant.
	// size 320 = RFC 5321 max (64 local + @ + 255 domain).
	Email    string    `gorm:"size:320;not null;uniqueIndex" json:"email"`
	TenantID uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Role     string    `gorm:"size:16;not null;default:'member'" json:"role"`

	CreatedAt time.Time `json:"created_at"`

	// Tenant belongsTo — FK on tenant_id with ON DELETE CASCADE.
	Tenant *Tenant `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE" json:"-"`
}

func (TenantUser) TableName() string {
	return "tenant_users"
}
