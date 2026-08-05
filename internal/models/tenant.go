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

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Tenant) TableName() string {
	return "tenants"
}

// BootstrapTenantID is the well-known UUID for the default tenant.
// Existing data gets assigned to this tenant during migration.
var BootstrapTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
