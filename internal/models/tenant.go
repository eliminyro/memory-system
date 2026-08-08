package models

import (
	"time"

	"github.com/google/uuid"
)

// Staleness mode constants for per-tenant enforcement level.
const (
	StalenessModeOff      = "off"
	StalenessModeAdvisory = "advisory"
	StalenessModeHard     = "hard"
)

// ValidStalenessModes is the accepted set for Tenant.StalenessMode.
var ValidStalenessModes = map[string]struct{}{
	StalenessModeOff:      {},
	StalenessModeAdvisory: {},
	StalenessModeHard:     {},
}

// TenantDefaults is the operator-chosen baseline for the three per-tenant
// toggles. It is the single shared shape for these values across config parsing
// and the service create-path.
type TenantDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

// BaselineTenantDefaults is the built-in safe-retention bundle used when the
// operator sets no MEMORY_DEFAULT_OPTS override: staleness_mode=hard,
// duplicate_guard=true, cleanup_scan_enabled=true.
func BaselineTenantDefaults() TenantDefaults {
	return TenantDefaults{StalenessMode: StalenessModeHard, DuplicateGuard: true, CleanupScanEnabled: true}
}

// Tenant type constants. A display/visibility classifier only — see Tenant.Type.
const (
	TenantTypePersonal = "personal"
	TenantTypeShared   = "shared"
)

// ValidTenantTypes is the accepted set for Tenant.Type.
var ValidTenantTypes = map[string]struct{}{
	TenantTypePersonal: {},
	TenantTypeShared:   {},
}

// IsValidTenantType reports whether t is an accepted tenant type
// (personal or shared).
func IsValidTenantType(t string) bool {
	_, ok := ValidTenantTypes[t]
	return ok
}

// Self-service policy constants. The optional lock over the two self-service
// surfaces (feature-toggle editing, API-key creation): "open" keeps today's
// member/owner self-service; "admin_only" raises both to admin.
const (
	SelfServicePolicyOpen      = "open"
	SelfServicePolicyAdminOnly = "admin_only"
)

// ValidSelfServicePolicies is the accepted set for the self-service policy —
// both the global config default and the per-tenant override.
var ValidSelfServicePolicies = map[string]struct{}{
	SelfServicePolicyOpen:      {},
	SelfServicePolicyAdminOnly: {},
}

// IsValidSelfServicePolicy reports whether p is an accepted self-service policy.
func IsValidSelfServicePolicy(p string) bool {
	_, ok := ValidSelfServicePolicies[p]
	return ok
}

type Tenant struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name  string    `gorm:"size:200;not null;uniqueIndex" json:"name"`
	Email string    `gorm:"size:200" json:"email,omitempty"`

	// Type is a DISPLAY-ONLY classifier ("personal" | "shared") for grouping
	// tenants in the UI. It MUST NOT be read by authorization: internal/authz
	// and authorize/Check never import or inspect this field, and access
	// decisions are identical regardless of its value. New tenants default to
	// "shared"; the NOT NULL DEFAULT backfills existing rows (incl. the default
	// pool) to "shared" on AutoMigrate.
	Type string `gorm:"type:text;not null;default:'shared'" json:"type"`

	// Per-tenant feature toggles. All default to the safest behavior so a tenant
	// upgrading from pre-tightening infra sees no change unless it opts in.
	StalenessMode      string `gorm:"size:16;not null;default:'off'" json:"staleness_mode"`
	DuplicateGuard     bool   `gorm:"not null;default:false" json:"duplicate_guard"`
	CleanupScanEnabled bool   `gorm:"not null;default:false" json:"cleanup_scan_enabled"`

	// SelfServicePolicy is the per-tenant override of the global self-service
	// gate: NULL = inherit the global default; else "open" | "admin_only". Set
	// and cleared by system admins only — never self-editable.
	SelfServicePolicy *string `gorm:"column:self_service_policy" json:"self_service_policy"`

	// EffectivePolicy is the resolved self-service policy (override ?? global),
	// computed on read paths — never persisted (gorm:"-").
	EffectivePolicy string `gorm:"-" json:"effective_self_service_policy,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveSelfServicePolicy resolves the tenant's effective self-service
// policy: the per-tenant override when set and valid, else the global default
// when valid, else "open" (so unset everywhere means open — today's behavior).
func (t Tenant) EffectiveSelfServicePolicy(globalDefault string) string {
	if t.SelfServicePolicy != nil && IsValidSelfServicePolicy(*t.SelfServicePolicy) {
		return *t.SelfServicePolicy
	}
	if IsValidSelfServicePolicy(globalDefault) {
		return globalDefault
	}
	return SelfServicePolicyOpen
}

func (Tenant) TableName() string {
	return "tenants"
}

// BootstrapTenantID is the well-known UUID for the default tenant.
// Existing data gets assigned to this tenant during migration.
var BootstrapTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
