package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v10"

	"github.com/eliminyro/memory-system/internal/service"
)

type Config struct {
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://memory:memory@localhost:5432/memory?sslmode=disable"`
	ServerAddr  string `env:"SERVER_ADDR" envDefault:":8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`

	// Embedding provider: "ollama" or "gcp"
	EmbeddingProvider   string `env:"EMBEDDING_PROVIDER" envDefault:"ollama"`
	EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" envDefault:"1024"`

	// Ollama
	OllamaURL   string `env:"OLLAMA_URL" envDefault:"http://localhost:11434"`
	OllamaModel string `env:"OLLAMA_MODEL" envDefault:"nomic-embed-text"`

	// GCP Vertex AI
	GCPProject  string `env:"GCP_PROJECT"`
	GCPLocation string `env:"GCP_LOCATION" envDefault:"us-central1"`
	GCPModel    string `env:"GCP_EMBEDDING_MODEL" envDefault:"text-embedding-005"`

	// Admin
	AdminAllowedEmails string `env:"ADMIN_ALLOWED_EMAILS"`

	// Cleanup pipeline — nightly lint scan populates cleanup_queue with
	// near-duplicate candidates. When TELEGRAM_* are set, a summary is posted
	// after each scan. All cleanup knobs are optional: leaving them empty
	// disables the respective feature gracefully.
	CleanupIntervalHours int    `env:"CLEANUP_INTERVAL_HOURS" envDefault:"24"`
	CleanupEnabled       bool   `env:"CLEANUP_ENABLED" envDefault:"true"`
	TelegramBotToken     string `env:"TELEGRAM_BOT_TOKEN"`
	TelegramChatID       string `env:"TELEGRAM_CHAT_ID"`

	// Tenant-toggle defaults. The raw spec comes in via env; cmd/server can
	// override via --opts before validation. ParseTenantDefaults turns the spec
	// into the typed TenantDefaults applied at AutoMigrate and tenant-create time.
	TenantDefaultsSpec string `env:"MEMORY_DEFAULT_OPTS"`
	TenantDefaults     TenantDefaults

	// authlet — OAuth 2.1 / OIDC authorization server for the /mcp endpoint.
	// AuthletMasterKey is a 32-byte hex-encoded key used to encrypt the
	// AS signing-key material at rest. GoogleClientID/Secret identify
	// memory-mcp as an OAuth client to Google (the upstream IdP); these are
	// distinct from any other deployment's Google client.
	//
	// When both Google client envs are set the operator has opted into the
	// authlet path and authletas.Setup must succeed at boot — any error
	// (including a malformed master key) is fatal. When the Google envs
	// are unset, /mcp serves API-key auth only and authlet is skipped.
	AuthletMasterKey   string `env:"AUTHLET_MASTER_KEY"`
	GoogleClientID     string `env:"MEMORY_MCP_GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"MEMORY_MCP_GOOGLE_CLIENT_SECRET"`
}

// TenantDefaults is the operator-chosen baseline for the three per-tenant
// feature toggles. Applied as the Postgres column DEFAULT after AutoMigrate
// and as the zero-value fill in service.CreateTenant.
type TenantDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

// DefaultTenantDefaults returns the safe baseline: every toggle off.
// A fresh deploy with no operator config picks this up.
func DefaultTenantDefaults() TenantDefaults {
	return TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false}
}

// ParseTenantDefaults turns "staleness=off,duplicate_guard=false,cleanup_scan_enabled=false"
// into a TenantDefaults. Empty input yields the safe baseline. Whitespace is
// tolerated, values are case-insensitive. Unknown keys or invalid values fail.
func ParseTenantDefaults(spec string) (TenantDefaults, error) {
	out := DefaultTenantDefaults()
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return out, nil
	}

	for pair := range strings.SplitSeq(spec, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return TenantDefaults{}, fmt.Errorf("expected key=value, got %q", strings.TrimSpace(pair))
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.ToLower(strings.TrimSpace(kv[1]))
		if key == "" {
			return TenantDefaults{}, fmt.Errorf("expected key=value, got %q", strings.TrimSpace(pair))
		}
		switch key {
		case "staleness":
			switch val {
			case "off", "advisory", "hard":
				out.StalenessMode = val
			default:
				return TenantDefaults{}, fmt.Errorf("invalid staleness value %q (want off|advisory|hard)", val)
			}
		case "duplicate_guard":
			b, err := parseBool(val)
			if err != nil {
				return TenantDefaults{}, fmt.Errorf("invalid duplicate_guard value %q (want true|false|1|0)", val)
			}
			out.DuplicateGuard = b
		case "cleanup_scan_enabled":
			b, err := parseBool(val)
			if err != nil {
				return TenantDefaults{}, fmt.Errorf("invalid cleanup_scan_enabled value %q (want true|false|1|0)", val)
			}
			out.CleanupScanEnabled = b
		default:
			return TenantDefaults{}, fmt.Errorf("unknown key %q (want staleness|duplicate_guard|cleanup_scan_enabled)", key)
		}
	}
	return out, nil
}

// parseBool accepts strconv.ParseBool inputs (true/false/1/0/etc) plus enforces
// case-insensitivity via lower-casing upstream.
func parseBool(s string) (bool, error) {
	return strconv.ParseBool(s)
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	td, err := ParseTenantDefaults(cfg.TenantDefaultsSpec)
	if err != nil {
		return nil, fmt.Errorf("parse MEMORY_DEFAULT_OPTS: %w", err)
	}
	cfg.TenantDefaults = td

	// MEMORY_MCP_GOOGLE_CLIENT_ID and MEMORY_MCP_GOOGLE_CLIENT_SECRET are
	// the OAuth opt-in signal. One without the other is a deployment bug:
	// authlet's OIDC discovery against Google needs both to succeed.
	if (cfg.GoogleClientID == "") != (cfg.GoogleClientSecret == "") {
		return nil, fmt.Errorf("MEMORY_MCP_GOOGLE_CLIENT_ID and MEMORY_MCP_GOOGLE_CLIENT_SECRET must be set together")
	}

	return cfg, nil
}

// AuthletEnabled reports whether the operator has opted into the authlet
// OAuth path by setting both Google client envs. When true, the caller must
// require authletas.Setup to succeed (fail-fast on misconfig). When false,
// the legacy API-key-only path is the intended mode.
func (c *Config) AuthletEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// EmbeddingCfg converts config fields into a service.EmbeddingConfig.
func (c *Config) EmbeddingCfg() service.EmbeddingConfig {
	return service.EmbeddingConfig{
		Dimensions:  c.EmbeddingDimensions,
		OllamaURL:   c.OllamaURL,
		OllamaModel: c.OllamaModel,
		GCPProject:  c.GCPProject,
		GCPLocation: c.GCPLocation,
		GCPModel:    c.GCPModel,
	}
}
