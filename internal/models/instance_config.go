package models

import "time"

// InstanceConfig is the singleton row holding instance-wide global settings —
// keyed by InstanceConfigSingletonID (the embedding_metadata singleton pattern).
// It is deliberately general so the future web-UI global-config page extends it
// with more columns. AccessRetentionEnabled is the one global toggle (default
// off) that arms access-recency eviction across every non-bootstrap tenant.
type InstanceConfig struct {
	ID                     uint      `gorm:"primaryKey" json:"id"`
	AccessRetentionEnabled bool      `gorm:"not null;default:false" json:"access_retention_enabled"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// InstanceConfigSingletonID is the fixed primary key of the single config row.
const InstanceConfigSingletonID = 1

func (InstanceConfig) TableName() string { return "instance_config" }
