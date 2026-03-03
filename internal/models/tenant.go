package models

import (
	"time"

	"github.com/google/uuid"
)

type Tenant struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name      string    `gorm:"size:200;not null;uniqueIndex" json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Tenant) TableName() string {
	return "tenants"
}

// BootstrapTenantID is the well-known UUID for the default tenant.
// Existing data gets assigned to this tenant during migration.
var BootstrapTenantID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
