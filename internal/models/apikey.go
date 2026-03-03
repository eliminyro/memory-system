package models

import (
	"time"

	"github.com/google/uuid"
)

type APIKey struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	TenantID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"tenant_id"`
	KeyHash   string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	Label     string     `gorm:"size:200;not null" json:"label"`
	Prefix    string     `gorm:"size:8;not null" json:"prefix"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	Tenant *Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
}

func (APIKey) TableName() string {
	return "api_keys"
}
