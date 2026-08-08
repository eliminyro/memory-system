package models

import (
	"time"

	"github.com/google/uuid"
)

// Override type constants for OverrideLog entries.
const (
	OverrideTypeForceCreate    = "force_create"
	OverrideTypeForceRead      = "force_read"
	OverrideTypeSettingsChange = "settings_change"
)

// Tool name constants for OverrideLog entries.
const (
	OverrideToolStoreMemory            = "store_memory"
	OverrideToolGetDocument            = "get_document"
	OverrideToolSearchMemory           = "search_memory"
	OverrideToolUpdateSection          = "update_section"
	OverrideToolUpdateMyTenantSettings = "update_my_tenant_settings"
	OverrideToolUpdateTenantSettings   = "update_tenant_settings"
)

// OverrideLog records every force-override call against the guarded tools.
// Kept forever — cheap rows, valuable audit trail for detecting agent abuse.
type OverrideLog struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	TenantID     uuid.UUID  `gorm:"type:uuid;not null;index:idx_override_tenant_time,priority:1" json:"tenant_id"`
	Tool         string     `gorm:"size:32;not null" json:"tool"`
	TargetID     *uuid.UUID `gorm:"type:uuid" json:"target_id,omitempty"`
	OverrideType string     `gorm:"size:32;not null" json:"override_type"`
	Reason       string     `gorm:"type:text;not null" json:"reason"`
	APIKeyID     *uuid.UUID `gorm:"type:uuid" json:"api_key_id,omitempty"`
	CreatedAt    time.Time  `gorm:"index:idx_override_tenant_time,priority:2,sort:desc" json:"created_at"`
}

func (OverrideLog) TableName() string { return "override_log" }
