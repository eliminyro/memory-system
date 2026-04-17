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

type Tenant struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name  string    `gorm:"size:200;not null;uniqueIndex" json:"name"`
	Email string    `gorm:"size:200" json:"email,omitempty"`

	// Per-tenant feature toggles. All default to the safest, least-surprising
	// behavior so a tenant upgrading from pre-tightening infrastructure sees
	// no behavior change unless it opts in.
	StalenessMode      string `gorm:"size:16;not null;default:'advisory'" json:"staleness_mode"`
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
