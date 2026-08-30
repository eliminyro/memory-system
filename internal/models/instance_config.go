package models

import "time"

// InstanceConfig is the singleton row holding instance-wide global settings —
// keyed by InstanceConfigSingletonID (the embedding_metadata singleton pattern).
// Env seeds these at migrate time; a stored value then wins and the admin API edits them live.
type InstanceConfig struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// GlobalsSeeded guards the one-time env→DB seed of the runtime globals on an
	// existing singleton row; fresh rows are inserted already-seeded.
	GlobalsSeeded bool `gorm:"not null;default:false" json:"-"`

	// Instance toggles.
	HistoryEnabled bool `gorm:"not null;default:false" json:"history_enabled"`

	// Retrieval tuning.
	MMRLambda            float64 `gorm:"not null;default:0.5" json:"mmr_lambda"`
	StalenessPenalty     float64 `gorm:"not null;default:0.2" json:"staleness_penalty"`
	CandidatePool        int     `gorm:"not null;default:20" json:"candidate_pool"`
	SnippetChars         int     `gorm:"not null;default:400" json:"snippet_chars"`
	HistoryRetentionDays int     `gorm:"not null;default:90" json:"history_retention_days"`

	// New-tenant toggle defaults + the global near-duplicate cutoff.
	StalenessDefault      string  `gorm:"size:16;not null;default:'hard'" json:"staleness_default"`
	DuplicateGuardDefault bool    `gorm:"not null;default:true" json:"duplicate_guard_default"`
	CleanupScanDefault    bool    `gorm:"not null;default:true" json:"cleanup_scan_default"`
	DuplicateThreshold    float64 `gorm:"not null;default:0.85" json:"duplicate_threshold"`

	// Access / self-service (signup_domains + admin_emails are CSV). admin_emails
	// seeds bootstrap admins at startup; live grant/revoke is via the admin UI.
	SelfServicePolicy string `gorm:"size:16;not null;default:'open'" json:"self_service_policy"`
	SignupDomains     string `gorm:"type:text;not null;default:''" json:"signup_domains"`
	AdminEmails       string `gorm:"type:text;not null;default:''" json:"admin_emails"`

	// Maintenance.
	CleanupEnabled       bool `gorm:"not null;default:true" json:"cleanup_enabled"`
	CleanupIntervalHours int  `gorm:"not null;default:24" json:"cleanup_interval_hours"`

	// HTTP hardening.
	RateLimitRPS      float64 `gorm:"not null;default:20" json:"rate_limit_rps"`
	RateLimitBurst    int     `gorm:"not null;default:40" json:"rate_limit_burst"`
	TrustedProxyDepth int     `gorm:"not null;default:0" json:"trusted_proxy_depth"`
	MaxRequestBytes   int64   `gorm:"not null;default:1048576" json:"max_request_bytes"`

	// Logging + outbound webhook (empty disables).
	LogLevel   string `gorm:"size:16;not null;default:'info'" json:"log_level"`
	WebhookURL string `gorm:"type:text;not null;default:''" json:"webhook_url"`

	UpdatedAt time.Time `json:"updated_at"`
}

// InstanceConfigSingletonID is the fixed primary key of the single config row.
const InstanceConfigSingletonID = 1

func (InstanceConfig) TableName() string { return "instance_config" }

// InstanceConfigPatch is a partial update of the singleton: a nil field is
// omitted (unchanged), a non-nil field is applied. Decoded from the admin PATCH
// body and consumed by InstanceConfigRepository.Update.
type InstanceConfigPatch struct {
	MMRLambda             *float64 `json:"mmr_lambda"`
	StalenessPenalty      *float64 `json:"staleness_penalty"`
	CandidatePool         *int     `json:"candidate_pool"`
	SnippetChars          *int     `json:"snippet_chars"`
	HistoryEnabled        *bool    `json:"history_enabled"`
	HistoryRetentionDays  *int     `json:"history_retention_days"`
	StalenessDefault      *string  `json:"staleness_default"`
	DuplicateGuardDefault *bool    `json:"duplicate_guard_default"`
	CleanupScanDefault    *bool    `json:"cleanup_scan_default"`
	DuplicateThreshold    *float64 `json:"duplicate_threshold"`
	SelfServicePolicy     *string  `json:"self_service_policy"`
	SignupDomains         *string  `json:"signup_domains"`
	AdminEmails           *string  `json:"admin_emails"`
	CleanupEnabled        *bool    `json:"cleanup_enabled"`
	CleanupIntervalHours  *int     `json:"cleanup_interval_hours"`
	RateLimitRPS          *float64 `json:"rate_limit_rps"`
	RateLimitBurst        *int     `json:"rate_limit_burst"`
	TrustedProxyDepth     *int     `json:"trusted_proxy_depth"`
	MaxRequestBytes       *int64   `json:"max_request_bytes"`
	LogLevel              *string  `json:"log_level"`
	WebhookURL            *string  `json:"webhook_url"`
}
