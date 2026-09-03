package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v10"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// DefaultUIClientID is the client_id assigned to the web UI's public PKCE OAuth
// client when MEMORY_UI_CLIENT_ID is unset on an OAuth-enabled instance. It
// keeps /ui/config.json from ever advertising an empty client and gives the
// boot-time seed (internal/authletstore.SeedUIClient) a stable id to register.
const DefaultUIClientID = "memory-ui"

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
	// near-duplicate candidates. These env values seed instance_config at migrate
	// time; the runtime reads them live via the accessor (webhook_url is DB-only).
	CleanupIntervalHours int  `env:"CLEANUP_INTERVAL_HOURS" envDefault:"24"`
	CleanupEnabled       bool `env:"CLEANUP_ENABLED" envDefault:"true"`

	// HistoryRetentionDays bounds mutation_history growth: the sweep prunes rows
	// older than this. Only matters when the global history toggle is on. 0 = never
	// prune (keep full history); the sweep's `days > 0` gate skips pruning entirely.
	HistoryRetentionDays int `env:"MEMORY_HISTORY_RETENTION_DAYS" envDefault:"90"`

	// HTTP hardening. MaxRequestBytes caps request bodies (0 disables). RateLimit*
	// is a token-bucket throttle over the auth+write surface (RPS <= 0 disables).
	// RateLimitTrustedProxyDepth is how many trusted reverse-proxy/CDN hops sit in
	// front: 0 (default) trusts none and keys on RemoteAddr (X-Forwarded-For is
	// ignored, unspoofable); N>=1 keys on the Nth-from-last X-Forwarded-For entry.
	MaxRequestBytes            int64   `env:"MAX_REQUEST_BYTES" envDefault:"1048576"`
	RateLimitRPS               float64 `env:"RATE_LIMIT_RPS" envDefault:"20"`
	RateLimitBurst             int     `env:"RATE_LIMIT_BURST" envDefault:"40"`
	RateLimitTrustedProxyDepth int     `env:"RATE_LIMIT_TRUSTED_PROXY_DEPTH" envDefault:"0"`

	// RequireConfigListener seeds instance_config.require_config_listener: when the
	// config-invalidation listener is dead, /~/ready fails only if this is on. Off
	// by default (single replica needs no peer convergence); multi-replica sets it.
	RequireConfigListener bool `env:"REQUIRE_CONFIG_LISTENER" envDefault:"false"`

	// Tenant-toggle defaults. Raw spec from env, overridable via --opts;
	// ParseTenantDefaults yields the typed models.TenantDefaults applied at
	// AutoMigrate and tenant-create time.
	TenantDefaultsSpec string `env:"MEMORY_DEFAULT_OPTS"`
	TenantDefaults     models.TenantDefaults

	// SelfServicePolicy is the global default self-service gate: "open" (default)
	// lets any member edit tenant toggles and an owner self-create API keys;
	// "admin_only" raises both to admin. A nullable per-tenant column overrides
	// it. Validated at load — unknown values are rejected.
	SelfServicePolicy string `env:"MEMORY_SELF_SERVICE_POLICY" envDefault:"open"`

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

	// SIGNUP_ALLOWED_DOMAINS gates self-serve tenant provisioning: a
	// comma-separated allow-list of email domains (e.g. "example.com,acme.org")
	// whose verified identities may auto-provision a personal tenant on first
	// login. Entries are lowercased and trimmed at load into SignupAllowedDomains.
	// Empty/unset ⇒ empty slice, meaning PUBLIC (any verified identity may
	// self-provision) — see design decision 2.
	SignupAllowedDomainsSpec string `env:"SIGNUP_ALLOWED_DOMAINS"`
	SignupAllowedDomains     []string

	// PublicBaseURL is the external origin (scheme+host, no path/trailing slash),
	// e.g. "https://mem.example.org". Anchors the authlet issuer/audience/PRM/
	// callback URLs and the UI OAuth config. REQUIRED (absolute http(s)) when the
	// authlet path is enabled; unused by the API-key-only path.
	PublicBaseURL string `env:"PUBLIC_BASE_URL"`

	// Reset — MemoryReset is a boot-time signal (never a route) that re-arms
	// bootstrap by clearing the admin-key set only. The first-run bootstrap token
	// is no longer configured via env: cmd/server/main.go generates and logs it on
	// an un-bootstrapped instance (design D1; see MemoryService.BootstrapToken).
	MemoryReset bool `env:"MEMORY_RESET"`

	// Import jobs — bounds on the async document-import path. MaxUploadBytes caps
	// the archive accepted by POST /api/admin/import (default 32 MiB).
	// WorkerConcurrency bounds the in-process worker draining import_jobs.
	ImportMaxUploadBytes    int64 `env:"IMPORT_MAX_UPLOAD_BYTES" envDefault:"33554432"`
	ImportWorkerConcurrency int   `env:"IMPORT_WORKER_CONCURRENCY" envDefault:"1"`

	// MMRLambda tunes HybridSearch's MMR diversity re-rank (repository.SearchParams.MMRLambda).
	// Default 0.5 = LongMemEval-tuned optimum (peak full-recall); 1.0 disables (pure relevance).
	MMRLambda float64 `env:"MEMORY_MMR_LAMBDA" envDefault:"0.5"`

	// StalenessPenalty down-weights stale docs in HybridSearch's re-rank
	// (repository.SearchParams). 0 = off (identity); 1 = strongest demotion.
	StalenessPenalty float64 `env:"MEMORY_STALENESS_PENALTY" envDefault:"0.2"`

	// SnippetChars caps the match-centered window search_memory returns when
	// snippet=true (approximate on the low end — ts_headline windows by words).
	SnippetChars int `env:"MEMORY_SNIPPET_CHARS" envDefault:"400"`

	// CandidatePool is the per-list SQL LIMIT each of HybridSearch's semantic and
	// lexical candidate lists draws before fusion; its half sets the tier cut.
	CandidatePool int `env:"MEMORY_CANDIDATE_POOL" envDefault:"20"`
}

// maxCandidatePool bounds MEMORY_CANDIDATE_POOL: MMR is O(n²) over 2*pool, so an
// absurd value is rejected at boot rather than silently melting search latency.
const maxCandidatePool = 1000

// ParseTenantDefaults parses "staleness=off,duplicate_guard=false,cleanup_scan_enabled=false"
// into a models.TenantDefaults, overlaying set keys on top of the built-in safe
// bundle (models.BaselineTenantDefaults). Empty = the safe bundle;
// whitespace-tolerant, case-insensitive; unknown keys or invalid values error.
func ParseTenantDefaults(spec string) (models.TenantDefaults, error) {
	out := models.BaselineTenantDefaults()
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return out, nil
	}

	for pair := range strings.SplitSeq(spec, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return models.TenantDefaults{}, fmt.Errorf("expected key=value, got %q", strings.TrimSpace(pair))
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.ToLower(strings.TrimSpace(kv[1]))
		if key == "" {
			return models.TenantDefaults{}, fmt.Errorf("expected key=value, got %q", strings.TrimSpace(pair))
		}
		switch key {
		case "staleness":
			switch val {
			case "off", "advisory", "hard":
				out.StalenessMode = val
			default:
				return models.TenantDefaults{}, fmt.Errorf("invalid staleness value %q (want off|advisory|hard)", val)
			}
		case "duplicate_guard":
			b, err := parseBool(val)
			if err != nil {
				return models.TenantDefaults{}, fmt.Errorf("invalid duplicate_guard value %q (want true|false|1|0)", val)
			}
			out.DuplicateGuard = b
		case "cleanup_scan_enabled":
			b, err := parseBool(val)
			if err != nil {
				return models.TenantDefaults{}, fmt.Errorf("invalid cleanup_scan_enabled value %q (want true|false|1|0)", val)
			}
			out.CleanupScanEnabled = b
		default:
			return models.TenantDefaults{}, fmt.Errorf("unknown key %q (want staleness|duplicate_guard|cleanup_scan_enabled)", key)
		}
	}
	return out, nil
}

// parseBool accepts strconv.ParseBool inputs (true/false/1/0); callers lower-case first.
func parseBool(s string) (bool, error) {
	return strconv.ParseBool(s)
}

// ParseAllowedDomains splits a comma-separated SIGNUP_ALLOWED_DOMAINS spec into
// a normalized allow-list: each entry lowercased and trimmed, empties dropped.
// An empty/whitespace-only spec yields nil (len 0), which callers treat as
// "public" — any verified identity may self-provision.
func ParseAllowedDomains(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(spec, ",") {
		d := strings.ToLower(strings.TrimSpace(part))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
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

	// Global default self-service policy (open|admin_only) — reject unknown values.
	if err := ValidateSelfServicePolicy(cfg.SelfServicePolicy); err != nil {
		return nil, err
	}

	// Self-serve signup domain allow-list (empty ⇒ public).
	cfg.SignupAllowedDomains = ParseAllowedDomains(cfg.SignupAllowedDomainsSpec)

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
		// Default the UI client id so /ui/config.json never advertises an empty
		// client on an OAuth instance and the boot-time UI-client seed has a
		// stable id to register.
		if cfg.UIClientID == "" {
			cfg.UIClientID = DefaultUIClientID
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
	case "gcp":
		if cfg.GCPProject == "" {
			return nil, fmt.Errorf("GCP_PROJECT is required when EMBEDDING_PROVIDER=gcp")
		}
	}

	if err := ValidateHistoryRetentionDays(cfg.HistoryRetentionDays); err != nil {
		return nil, err
	}

	// HTTP hardening bounds.
	if err := ValidateMaxRequestBytes(cfg.MaxRequestBytes); err != nil {
		return nil, err
	}
	if err := ValidateRateLimit(cfg.RateLimitRPS, cfg.RateLimitBurst); err != nil {
		return nil, err
	}
	if cfg.RateLimitRPS > 0 && cfg.RateLimitTrustedProxyDepth == 0 {
		slog.Default().Warn("rate limiting is on but RATE_LIMIT_TRUSTED_PROXY_DEPTH=0: X-Forwarded-For is ignored and clients are keyed by RemoteAddr — behind a reverse proxy/CDN every client shares one bucket. Set it to the number of trusted proxy hops (e.g. 1 behind a single ingress).")
	}

	// MMR lambda must stay in (0, 1] — fail fast rather than silently clamp a bad value.
	if err := ValidateMMRLambda(cfg.MMRLambda); err != nil {
		return nil, err
	}

	// Staleness penalty in [0, 1] — 0 disables, 1 is the strongest demotion.
	if err := ValidateStalenessPenalty(cfg.StalenessPenalty); err != nil {
		return nil, err
	}

	// Snippet window must be positive — a zero/negative cap yields empty snippets.
	if err := ValidateSnippetChars(cfg.SnippetChars); err != nil {
		return nil, err
	}

	// Embedding dimensions must be positive — a non-positive value can't size the
	// vector column and every write would fail at insert.
	if cfg.EmbeddingDimensions <= 0 {
		return nil, fmt.Errorf("EMBEDDING_DIMENSIONS must be > 0, got %d", cfg.EmbeddingDimensions)
	}

	// Candidate pool is a per-list SQL LIMIT feeding the O(n²) MMR re-rank; a
	// non-positive value yields no candidates and an absurd one blows up cost.
	if err := ValidateCandidatePool(cfg.CandidatePool); err != nil {
		return nil, err
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
