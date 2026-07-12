package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v10"

	"github.com/eliminyro/memory-system/internal/service"
)

type Config struct {
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://memory:memory@localhost:5432/memory?sslmode=disable"`
	ServerAddr  string `env:"SERVER_ADDR" envDefault:":8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`

	// Embedding provider: "ollama", "gcp", "openai", "aws", or "fake". Default
	// dimension matches the default model (ollama nomic-embed-text=768) so a stock
	// deploy works (audit #12). For OpenAI text-embedding-3-*, also the output size.
	EmbeddingProvider   string `env:"EMBEDDING_PROVIDER" envDefault:"ollama"`
	EmbeddingDimensions int    `env:"EMBEDDING_DIMENSIONS" envDefault:"768"`

	// Ollama
	OllamaURL   string `env:"OLLAMA_URL" envDefault:"http://localhost:11434"`
	OllamaModel string `env:"OLLAMA_MODEL" envDefault:"nomic-embed-text"`

	// GCP Vertex AI
	GCPProject  string `env:"GCP_PROJECT"`
	GCPLocation string `env:"GCP_LOCATION" envDefault:"us-central1"`
	GCPModel    string `env:"GCP_EMBEDDING_MODEL" envDefault:"text-embedding-005"`

	// OpenAI-compatible — any /v1/embeddings endpoint (OpenAI, Azure, vLLM, TEI,
	// etc). APIKey optional (self-hosted often ignores it); BaseURL is the API root.
	OpenAIBaseURL string `env:"OPENAI_BASE_URL" envDefault:"https://api.openai.com/v1"`
	OpenAIAPIKey  string `env:"OPENAI_API_KEY"`
	OpenAIModel   string `env:"OPENAI_EMBEDDING_MODEL"`

	// AWS Bedrock. Credentials resolve from the standard AWS chain (env vars,
	// shared config, IAM role) — never from these fields. Region + model only.
	AWSRegion string `env:"AWS_REGION"`
	AWSModel  string `env:"AWS_EMBEDDING_MODEL"`

	// Admin
	AdminAllowedEmails string `env:"ADMIN_ALLOWED_EMAILS"`

	// Cleanup pipeline — nightly lint scan populates cleanup_queue with
	// near-duplicate candidates; TELEGRAM_* posts a per-scan summary. All knobs
	// optional (empty disables the feature).
	CleanupIntervalHours int    `env:"CLEANUP_INTERVAL_HOURS" envDefault:"24"`
	CleanupEnabled       bool   `env:"CLEANUP_ENABLED" envDefault:"true"`
	TelegramBotToken     string `env:"TELEGRAM_BOT_TOKEN"`
	TelegramChatID       string `env:"TELEGRAM_CHAT_ID"`

	// Retention sweep — archives docs unverified past multiplier × the doc_type
	// staleness threshold, then hard-deletes deleteGraceDays after archiving. Only
	// for staleness_mode=hard tenants. Both must be >= 1: below 1 collapses the
	// cutoffs and would mass hard-delete live data (Load rejects; retainTenant also guards).
	RetentionMultiplier int `env:"RETENTION_MULTIPLIER" envDefault:"3"`
	DeleteGraceDays     int `env:"RETENTION_DELETE_GRACE_DAYS" envDefault:"30"`

	// HTTP hardening. MaxRequestBytes caps request bodies (0 disables). RateLimit*
	// is a token-bucket throttle over the auth+write surface (RPS <= 0 disables).
	MaxRequestBytes int64   `env:"MAX_REQUEST_BYTES" envDefault:"1048576"`
	RateLimitRPS    float64 `env:"RATE_LIMIT_RPS" envDefault:"20"`
	RateLimitBurst  int     `env:"RATE_LIMIT_BURST" envDefault:"40"`

	// Tenant-toggle defaults. Raw spec from env, overridable via --opts;
	// ParseTenantDefaults yields the typed TenantDefaults applied at AutoMigrate
	// and tenant-create time.
	TenantDefaultsSpec string `env:"MEMORY_DEFAULT_OPTS"`
	TenantDefaults     TenantDefaults

	// authlet — OAuth 2.1 / OIDC AS for /mcp. AuthletMasterKey is a 32-byte hex
	// key encrypting AS signing material at rest. GoogleClient* identify memory-mcp
	// to Google (upstream IdP). Both Google envs set = opt into authlet: Setup must
	// succeed at boot (any error fatal). Unset = /mcp is API-key-only, authlet skipped.
	AuthletMasterKey   string `env:"AUTHLET_MASTER_KEY"`
	GoogleClientID     string `env:"MEMORY_MCP_GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"MEMORY_MCP_GOOGLE_CLIENT_SECRET"`

	// UIClientID is the pre-registered public PKCE OAuth client the web UI uses
	// (redirect_uri = PublicBaseURL + "/ui"). Non-secret; served to the page.
	UIClientID string `env:"MEMORY_UI_CLIENT_ID"`

	// PublicBaseURL is the external origin (scheme+host, no path/trailing slash),
	// e.g. "https://mem.example.org". Anchors the authlet issuer/audience/PRM/
	// callback URLs and the UI OAuth config. REQUIRED (absolute http(s)) when the
	// authlet path is enabled; unused by the API-key-only path.
	PublicBaseURL string `env:"PUBLIC_BASE_URL"`
}

// TenantDefaults is the operator baseline for the three per-tenant toggles,
// applied as the Postgres column DEFAULT (AutoMigrate) and in service.CreateTenant.
type TenantDefaults struct {
	StalenessMode      string
	DuplicateGuard     bool
	CleanupScanEnabled bool
}

// DefaultTenantDefaults returns the safe baseline: every toggle off.
func DefaultTenantDefaults() TenantDefaults {
	return TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false}
}

// ParseTenantDefaults parses "staleness=off,duplicate_guard=false,cleanup_scan_enabled=false"
// into a TenantDefaults. Empty = safe baseline; whitespace-tolerant, case-insensitive;
// unknown keys or invalid values error.
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

// parseBool accepts strconv.ParseBool inputs (true/false/1/0); callers lower-case first.
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

	// Both Google client envs are the OAuth opt-in signal; one without the other
	// is a deployment bug (OIDC discovery needs both).
	if (cfg.GoogleClientID == "") != (cfg.GoogleClientSecret == "") {
		return nil, fmt.Errorf("MEMORY_MCP_GOOGLE_CLIENT_ID and MEMORY_MCP_GOOGLE_CLIENT_SECRET must be set together")
	}

	// Normalize away a trailing slash so derived URLs (base+"/mcp") are well-formed.
	// Mandatory when authlet is enabled — empty issuer/redirect_uri would silently
	// break OAuth.
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if cfg.AuthletEnabled() {
		if err := validatePublicBaseURL(cfg.PublicBaseURL); err != nil {
			return nil, err
		}
	}

	// Provider-specific required fields — fail fast at boot, not at first embed.
	switch cfg.EmbeddingProvider {
	case "openai":
		if cfg.OpenAIModel == "" {
			return nil, fmt.Errorf("OPENAI_EMBEDDING_MODEL is required when EMBEDDING_PROVIDER=openai")
		}
	case "aws":
		if cfg.AWSRegion == "" || cfg.AWSModel == "" {
			return nil, fmt.Errorf("AWS_REGION and AWS_EMBEDDING_MODEL are required when EMBEDDING_PROVIDER=aws")
		}
	}

	// Retention lower bounds — reject values that would mass-delete live data (audit #4).
	if cfg.RetentionMultiplier < 1 {
		return nil, fmt.Errorf("RETENTION_MULTIPLIER must be >= 1, got %d", cfg.RetentionMultiplier)
	}
	if cfg.DeleteGraceDays < 1 {
		return nil, fmt.Errorf("RETENTION_DELETE_GRACE_DAYS must be >= 1, got %d", cfg.DeleteGraceDays)
	}

	// HTTP hardening bounds.
	if cfg.MaxRequestBytes < 0 {
		return nil, fmt.Errorf("MAX_REQUEST_BYTES must be >= 0, got %d", cfg.MaxRequestBytes)
	}
	if cfg.RateLimitRPS > 0 && cfg.RateLimitBurst < 1 {
		return nil, fmt.Errorf("RATE_LIMIT_BURST must be >= 1 when RATE_LIMIT_RPS > 0, got %d", cfg.RateLimitBurst)
	}

	return cfg, nil
}

// AuthletEnabled reports whether both Google client envs are set (opt-in to the
// authlet OAuth path). When true, callers must require authletas.Setup to succeed.
func (c *Config) AuthletEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// validatePublicBaseURL enforces an absolute http(s) origin with a host and no
// path — the shape authlet issuer/audience/PRM derivations assume.
func validatePublicBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("PUBLIC_BASE_URL is required when the authlet OAuth path is enabled (MEMORY_MCP_GOOGLE_CLIENT_ID/SECRET set)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("PUBLIC_BASE_URL is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("PUBLIC_BASE_URL must use http or https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("PUBLIC_BASE_URL must include a host, got %q", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("PUBLIC_BASE_URL must not include a path, got %q", u.Path)
	}
	return nil
}

// EmbeddingModel returns the active provider's model id. With EmbeddingProvider
// it fingerprints the corpus's embedding identity for the migration guard (audit #13/#16).
func (c *Config) EmbeddingModel() string {
	switch c.EmbeddingProvider {
	case "gcp":
		return c.GCPModel
	case "ollama":
		return c.OllamaModel
	case "openai":
		return c.OpenAIModel
	case "aws":
		return c.AWSModel
	default:
		return c.EmbeddingProvider
	}
}

// EmbeddingCfg converts config fields into a service.EmbeddingConfig.
func (c *Config) EmbeddingCfg() service.EmbeddingConfig {
	return service.EmbeddingConfig{
		Dimensions:    c.EmbeddingDimensions,
		OllamaURL:     c.OllamaURL,
		OllamaModel:   c.OllamaModel,
		GCPProject:    c.GCPProject,
		GCPLocation:   c.GCPLocation,
		GCPModel:      c.GCPModel,
		OpenAIBaseURL: c.OpenAIBaseURL,
		OpenAIAPIKey:  c.OpenAIAPIKey,
		OpenAIModel:   c.OpenAIModel,
		AWSRegion:     c.AWSRegion,
		AWSModel:      c.AWSModel,
	}
}
