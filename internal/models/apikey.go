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

	// ExpiresAt: instant after which the key stops authenticating (auth.ValidateKey).
	// NULL = never expires. Set at issue (--ttl) or by rotation's grace window.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// LastUsedAt: best-effort last successful validation (errors ignored). NULL =
	// never used. Admin listing uses it to spot stale keys.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	// SubjectID pins the key to a unified authz subject. NULL = tenant service
	// principal ("svc:<tenant_id>"); set = resolved subject id per request.
	SubjectID *string `gorm:"size:255;index" json:"subject_id,omitempty"`

	Tenant *Tenant `gorm:"foreignKey:TenantID" json:"tenant,omitempty"`
}

func (APIKey) TableName() string {
	return "api_keys"
}
