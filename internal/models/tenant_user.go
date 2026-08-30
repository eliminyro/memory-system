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

// TenantUser maps an OIDC identity to a tenant. Identity is anchored on the
// (issuer, subject) pair; the authlet AS resolves it at sign-in and auto-
// provisions or adopts a mapping from verified federated claims.
type TenantUser struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	// Identity anchor: the OIDC (issuer, subject) pair, unique together. Nullable
	// so legacy email-only rows coexist (Postgres treats NULLs as distinct) until
	// they adopt a subject on first login.
	Issuer  *string `gorm:"size:255;uniqueIndex:idx_tenant_users_iss_sub" json:"issuer,omitempty"`
	Subject *string `gorm:"size:255;uniqueIndex:idx_tenant_users_iss_sub" json:"subject,omitempty"`
	// Email is a mutable attribute kept globally unique (one email -> one identity)
	// but no longer the identity key; it refreshes from the claim on login.
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
