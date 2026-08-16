package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eliminyro/memory-system/internal/models"
)

func TestParseTenantDefaults(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    models.TenantDefaults
		wantErr string
	}{
		{
			name:  "empty string yields safe bundle",
			input: "",
			want:  models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true},
		},
		{
			name:  "full opt-out overrides every toggle",
			input: "staleness=off,duplicate_guard=false,cleanup_scan_enabled=false",
			want:  models.TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "partial: staleness overrides, other toggles keep bundle",
			input: "staleness=advisory",
			want:  models.TenantDefaults{StalenessMode: "advisory", DuplicateGuard: true, CleanupScanEnabled: true},
		},
		{
			name:  "partial: duplicate_guard off, rest keep bundle",
			input: "duplicate_guard=false",
			want:  models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: false, CleanupScanEnabled: true},
		},
		{
			name:  "whitespace tolerated around tokens",
			input: "  staleness = advisory ,  duplicate_guard = false ",
			want:  models.TenantDefaults{StalenessMode: "advisory", DuplicateGuard: false, CleanupScanEnabled: true},
		},
		{
			name:  "staleness value is case-insensitive",
			input: "staleness=ADVISORY",
			want:  models.TenantDefaults{StalenessMode: "advisory", DuplicateGuard: true, CleanupScanEnabled: true},
		},
		{
			name:  "bool accepts true/false case-insensitive",
			input: "duplicate_guard=FALSE,cleanup_scan_enabled=False",
			want:  models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "bool accepts 1 and 0",
			input: "duplicate_guard=0,cleanup_scan_enabled=0",
			want:  models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:    "unknown key fails",
			input:   "foo=bar",
			wantErr: `unknown key "foo"`,
		},
		{
			name:    "invalid staleness value fails",
			input:   "staleness=loud",
			wantErr: `invalid staleness value "loud"`,
		},
		{
			name:    "invalid bool value fails",
			input:   "duplicate_guard=maybe",
			wantErr: `invalid duplicate_guard value "maybe"`,
		},
		{
			name:    "missing equals fails",
			input:   "staleness",
			wantErr: `expected key=value`,
		},
		{
			name:    "empty key fails",
			input:   "=off",
			wantErr: `expected key=value`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTenantDefaults(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLoadRequiresBothGoogleEnvsOrNeither(t *testing.T) {
	cases := []struct {
		name        string
		clientID    string
		clientSec   string
		baseURL     string
		wantErr     string
		wantEnabled bool
	}{
		{name: "neither set: ok, authlet disabled", wantEnabled: false},
		{name: "both set: ok, authlet enabled", clientID: "id", clientSec: "sec", baseURL: "https://mem.example.org", wantEnabled: true},
		{name: "only id set fails", clientID: "id", wantErr: "MEMORY_MCP_GOOGLE_CLIENT"},
		{name: "only secret set fails", clientSec: "sec", wantErr: "MEMORY_MCP_GOOGLE_CLIENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_ID", tc.clientID)
			t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_SECRET", tc.clientSec)
			t.Setenv("PUBLIC_BASE_URL", tc.baseURL)
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := cfg.AuthletEnabled(); got != tc.wantEnabled {
				t.Fatalf("AuthletEnabled=%v want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestLoadPublicBaseURL(t *testing.T) {
	cases := []struct {
		name        string
		authlet     bool // set both Google envs -> AuthletEnabled
		baseURL     string
		wantErr     string
		wantBaseURL string // expected normalized value when no error
	}{
		{name: "authlet on, valid https", authlet: true, baseURL: "https://mem.example.org", wantBaseURL: "https://mem.example.org"},
		{name: "authlet on, trailing slash trimmed", authlet: true, baseURL: "https://mem.example.org/", wantBaseURL: "https://mem.example.org"},
		{name: "authlet on, http allowed", authlet: true, baseURL: "http://localhost:8080", wantBaseURL: "http://localhost:8080"},
		{name: "authlet on, empty rejected", authlet: true, baseURL: "", wantErr: "PUBLIC_BASE_URL is required"},
		{name: "authlet on, bad scheme rejected", authlet: true, baseURL: "ftp://mem.example.org", wantErr: "http or https"},
		{name: "authlet on, path rejected", authlet: true, baseURL: "https://mem.example.org/base", wantErr: "must not include a path"},
		{name: "authlet off, empty ok", authlet: false, baseURL: "", wantBaseURL: ""},
		{name: "authlet off, no validation but still normalized", authlet: false, baseURL: "https://mem.example.org/", wantBaseURL: "https://mem.example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.authlet {
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_ID", "id")
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_SECRET", "sec")
			} else {
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_ID", "")
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_SECRET", "")
			}
			t.Setenv("PUBLIC_BASE_URL", tc.baseURL)

			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.PublicBaseURL != tc.wantBaseURL {
				t.Fatalf("PublicBaseURL = %q, want %q", cfg.PublicBaseURL, tc.wantBaseURL)
			}
		})
	}
}

func TestLoadDefaultsUIClientID(t *testing.T) {
	cases := []struct {
		name     string
		authlet  bool
		envValue string
		want     string
	}{
		{name: "authlet on, unset -> defaulted", authlet: true, envValue: "", want: DefaultUIClientID},
		{name: "authlet on, explicit value kept", authlet: true, envValue: "custom-ui", want: "custom-ui"},
		{name: "authlet off, unset stays empty", authlet: false, envValue: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.authlet {
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_ID", "id")
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_SECRET", "sec")
				t.Setenv("PUBLIC_BASE_URL", "https://mem.example.org")
			} else {
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_ID", "")
				t.Setenv("MEMORY_MCP_GOOGLE_CLIENT_SECRET", "")
			}
			t.Setenv("MEMORY_UI_CLIENT_ID", tc.envValue)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.UIClientID != tc.want {
				t.Fatalf("UIClientID = %q, want %q", cfg.UIClientID, tc.want)
			}
		})
	}
}

func TestLoadProviderRequiredFields(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		openaiM  string
		awsReg   string
		awsM     string
		gcpProj  string
		wantErr  string
	}{
		{name: "ollama default needs nothing", provider: "ollama"},
		{name: "openai with model ok", provider: "openai", openaiM: "text-embedding-3-small"},
		{name: "openai without model rejected", provider: "openai", wantErr: "OPENAI_EMBEDDING_MODEL is required"},
		{name: "aws with region+model ok", provider: "aws", awsReg: "us-east-1", awsM: "amazon.titan-embed-text-v2:0"},
		{name: "aws without region rejected", provider: "aws", awsM: "amazon.titan-embed-text-v2:0", wantErr: "AWS_REGION and AWS_EMBEDDING_MODEL"},
		{name: "aws without model rejected", provider: "aws", awsReg: "us-east-1", wantErr: "AWS_REGION and AWS_EMBEDDING_MODEL"},
		{name: "gcp with project ok", provider: "gcp", gcpProj: "my-project"},
		{name: "gcp without project rejected", provider: "gcp", wantErr: "GCP_PROJECT is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EMBEDDING_PROVIDER", tc.provider)
			t.Setenv("OPENAI_EMBEDDING_MODEL", tc.openaiM)
			t.Setenv("AWS_REGION", tc.awsReg)
			t.Setenv("AWS_EMBEDDING_MODEL", tc.awsM)
			t.Setenv("GCP_PROJECT", tc.gcpProj)

			_, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// RG4: rate limiting on with trusted-proxy depth 0 warns but must NOT fail the
// load — the default is spoof-safe when there is no proxy in front.
func TestLoadRateLimitDefaultProxyDepthWarnsNotFails(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "20")
	t.Setenv("RATE_LIMIT_TRUSTED_PROXY_DEPTH", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RateLimitTrustedProxyDepth != 0 {
		t.Fatalf("RateLimitTrustedProxyDepth = %d, want 0", cfg.RateLimitTrustedProxyDepth)
	}
}

func TestLoadRejectsRetentionBelowLowerBound(t *testing.T) {
	cases := []struct {
		name       string
		multiplier string
		grace      string
		wantErr    string
	}{
		{name: "defaults ok", multiplier: "3", grace: "30"},
		{name: "min ok", multiplier: "1", grace: "1"},
		{name: "multiplier 0 rejected", multiplier: "0", grace: "30", wantErr: "RETENTION_MULTIPLIER must be >= 1"},
		{name: "multiplier negative rejected", multiplier: "-2", grace: "30", wantErr: "RETENTION_MULTIPLIER must be >= 1"},
		{name: "grace 0 rejected", multiplier: "3", grace: "0", wantErr: "RETENTION_DELETE_GRACE_DAYS must be >= 1"},
		{name: "grace negative rejected", multiplier: "3", grace: "-1", wantErr: "RETENTION_DELETE_GRACE_DAYS must be >= 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RETENTION_MULTIPLIER", tc.multiplier)
			t.Setenv("RETENTION_DELETE_GRACE_DAYS", tc.grace)
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.RetentionMultiplier < 1 || cfg.DeleteGraceDays < 1 {
				t.Fatalf("loaded unsafe retention window: mult=%d grace=%d", cfg.RetentionMultiplier, cfg.DeleteGraceDays)
			}
		})
	}
}

func TestLoadSelfServicePolicy(t *testing.T) {
	cases := []struct {
		name    string
		value   string // "" means leave unset (exercise the envDefault)
		set     bool
		want    string
		wantErr string
	}{
		{name: "default is open", set: false, want: "open"},
		{name: "explicit open", value: "open", set: true, want: "open"},
		{name: "explicit admin_only", value: "admin_only", set: true, want: "admin_only"},
		{name: "unknown rejected", value: "locked", set: true, wantErr: "MEMORY_SELF_SERVICE_POLICY must be open or admin_only"},
		{name: "empty falls back to default open", value: "", set: true, want: "open"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("MEMORY_SELF_SERVICE_POLICY", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.SelfServicePolicy != tc.want {
				t.Fatalf("SelfServicePolicy = %q, want %q", cfg.SelfServicePolicy, tc.want)
			}
		})
	}
}

func TestBaselineTenantDefaults(t *testing.T) {
	got := models.BaselineTenantDefaults()
	want := models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestLoadTenantDefaultsBundle guards design decision 6: the built-in
// MEMORY_DEFAULT_OPTS default is the safe retention bundle, and an operator
// override via the env var still wins.
func TestLoadTenantDefaultsBundle(t *testing.T) {
	t.Run("built-in default is the safe bundle when unset", func(t *testing.T) {
		t.Setenv("MEMORY_DEFAULT_OPTS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		want := models.TenantDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true}
		if cfg.TenantDefaults != want {
			t.Fatalf("TenantDefaults = %+v, want %+v", cfg.TenantDefaults, want)
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("MEMORY_DEFAULT_OPTS", "staleness=off,duplicate_guard=false,cleanup_scan_enabled=false")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		want := models.TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false}
		if cfg.TenantDefaults != want {
			t.Fatalf("TenantDefaults = %+v, want %+v", cfg.TenantDefaults, want)
		}
	})
}

func TestParseAllowedDomains(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty is public", "", nil},
		{"whitespace-only is public", "   ", nil},
		{"single domain", "example.com", []string{"example.com"}},
		{"multiple domains", "example.com,acme.org", []string{"example.com", "acme.org"}},
		{"mixed case lowercased", "Example.COM,ACME.org", []string{"example.com", "acme.org"}},
		{"whitespace trimmed per entry", "  example.com , acme.org  ", []string{"example.com", "acme.org"}},
		{"empty entries dropped", "example.com,,acme.org,", []string{"example.com", "acme.org"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAllowedDomains(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseAllowedDomains(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseAllowedDomains(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestLoadParsesSignupAllowedDomains(t *testing.T) {
	t.Run("unset is public (empty slice)", func(t *testing.T) {
		t.Setenv("SIGNUP_ALLOWED_DOMAINS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(cfg.SignupAllowedDomains) != 0 {
			t.Fatalf("SignupAllowedDomains = %v, want empty", cfg.SignupAllowedDomains)
		}
	})

	t.Run("normalized allow-list", func(t *testing.T) {
		t.Setenv("SIGNUP_ALLOWED_DOMAINS", " Example.COM , acme.org ")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		want := []string{"example.com", "acme.org"}
		if len(cfg.SignupAllowedDomains) != len(want) {
			t.Fatalf("SignupAllowedDomains = %v, want %v", cfg.SignupAllowedDomains, want)
		}
		for i := range want {
			if cfg.SignupAllowedDomains[i] != want[i] {
				t.Fatalf("SignupAllowedDomains[%d] = %q, want %q", i, cfg.SignupAllowedDomains[i], want[i])
			}
		}
	})
}

// TestLoadDefaultEmbeddingDimensionsMatchesDefaultModel guards audit #12: a
// stock deploy uses the default ollama provider + nomic-embed-text (768-dim), so
// the default EMBEDDING_DIMENSIONS must be 768 or every write fails at insert.
func TestLoadDefaultEmbeddingDimensionsMatchesDefaultModel(t *testing.T) {
	orig, had := os.LookupEnv("EMBEDDING_DIMENSIONS")
	if err := os.Unsetenv("EMBEDDING_DIMENSIONS"); err != nil {
		t.Fatalf("unset env: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("EMBEDDING_DIMENSIONS", orig)
		}
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.EmbeddingDimensions != 768 {
		t.Fatalf("default EMBEDDING_DIMENSIONS = %d, want 768 (nomic-embed-text)", cfg.EmbeddingDimensions)
	}
	if cfg.EmbeddingProvider != "ollama" {
		t.Fatalf("default EMBEDDING_PROVIDER = %q, want ollama", cfg.EmbeddingProvider)
	}
}

// TestLoadRejectsMMRLambdaOutOfRange guards the fail-fast contract for
// MEMORY_MMR_LAMBDA: must be in (0, 1], no silent clamping.
func TestLoadRejectsMMRLambdaOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "default unset ok", value: ""},
		{name: "0.9 ok", value: "0.9"},
		{name: "1.0 ok (disables MMR)", value: "1.0"},
		{name: "zero rejected", value: "0", wantErr: "MEMORY_MMR_LAMBDA must be in (0, 1]"},
		{name: "negative rejected", value: "-0.5", wantErr: "MEMORY_MMR_LAMBDA must be in (0, 1]"},
		{name: "above 1 rejected", value: "1.5", wantErr: "MEMORY_MMR_LAMBDA must be in (0, 1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != "" {
				t.Setenv("MEMORY_MMR_LAMBDA", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.MMRLambda <= 0 || cfg.MMRLambda > 1 {
				t.Fatalf("loaded out-of-range MMRLambda: %v", cfg.MMRLambda)
			}
		})
	}
}

// TestLoadRejectsNonPositiveSnippetChars guards the fail-fast contract for
// MEMORY_SNIPPET_CHARS: must be > 0 (a zero/negative cap yields empty snippets).
// Default unset is 400.
func TestLoadRejectsNonPositiveSnippetChars(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "default unset ok (400)", value: ""},
		{name: "positive ok", value: "800"},
		{name: "zero rejected", value: "0", wantErr: "MEMORY_SNIPPET_CHARS must be > 0"},
		{name: "negative rejected", value: "-1", wantErr: "MEMORY_SNIPPET_CHARS must be > 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != "" {
				t.Setenv("MEMORY_SNIPPET_CHARS", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.SnippetChars <= 0 {
				t.Fatalf("loaded non-positive SnippetChars: %d", cfg.SnippetChars)
			}
			if tc.value == "" && cfg.SnippetChars != 400 {
				t.Fatalf("want default SnippetChars 400, got %d", cfg.SnippetChars)
			}
		})
	}
}

// TestLoadRejectsUsageWeightOutOfRange guards the fail-fast contract for
// MEMORY_USAGE_WEIGHT: must be in [0, 5], no silent clamping. 0 is the Phase-A
// no-op default.
func TestLoadRejectsUsageWeightOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "default unset ok", value: ""},
		{name: "zero ok (off)", value: "0"},
		{name: "5 ok (upper bound)", value: "5"},
		{name: "negative rejected", value: "-0.1", wantErr: "MEMORY_USAGE_WEIGHT must be in [0, 5]"},
		{name: "above 5 rejected", value: "5.1", wantErr: "MEMORY_USAGE_WEIGHT must be in [0, 5]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != "" {
				t.Setenv("MEMORY_USAGE_WEIGHT", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.UsageWeight < 0 || cfg.UsageWeight > 5 {
				t.Fatalf("loaded out-of-range UsageWeight: %v", cfg.UsageWeight)
			}
		})
	}
}

// TestLoadRejectsNonPositiveRecallReceiptTTL guards the fail-fast contract for
// MEMORY_RECALL_RECEIPT_TTL: must be > 0, or the prune silently never fires
// and the receipts table grows unbounded.
func TestLoadRejectsNonPositiveRecallReceiptTTL(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "default unset ok (72h)", value: ""},
		{name: "positive ok", value: "1h"},
		{name: "zero rejected", value: "0", wantErr: "MEMORY_RECALL_RECEIPT_TTL must be > 0"},
		{name: "negative rejected", value: "-1h", wantErr: "MEMORY_RECALL_RECEIPT_TTL must be > 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != "" {
				t.Setenv("MEMORY_RECALL_RECEIPT_TTL", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.RecallReceiptTTL <= 0 {
				t.Fatalf("loaded non-positive RecallReceiptTTL: %v", cfg.RecallReceiptTTL)
			}
		})
	}
}

// TestLoadRecallReconsolidationDefaults guards the Phase-A safe-landing
// defaults (design D7): receipts on, a 72h TTL, and both Phase B/C toggles off.
func TestLoadRecallReconsolidationDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.RecallReceipts {
		t.Error("RecallReceipts default = false, want true")
	}
	if cfg.RecallReceiptTTL != 72*time.Hour {
		t.Errorf("RecallReceiptTTL default = %v, want 72h", cfg.RecallReceiptTTL)
	}
	if cfg.UsageWeight != 0 {
		t.Errorf("UsageWeight default = %v, want 0", cfg.UsageWeight)
	}
	if cfg.UsageRetention {
		t.Error("UsageRetention default = true, want false")
	}
}

// TestEmbeddingModelResolvesPerProvider verifies the model fingerprint used by
// the migration swap guard (audit #13/#16) tracks the active provider.
func TestEmbeddingModelResolvesPerProvider(t *testing.T) {
	cases := []struct {
		provider, ollama, gcp, want string
	}{
		{"ollama", "nomic-embed-text", "text-embedding-005", "nomic-embed-text"},
		{"gcp", "nomic-embed-text", "text-embedding-005", "text-embedding-005"},
		{"fake", "nomic-embed-text", "text-embedding-005", "fake"},
	}
	for _, tc := range cases {
		c := &Config{EmbeddingProvider: tc.provider, OllamaModel: tc.ollama, GCPModel: tc.gcp}
		if got := c.EmbeddingModel(); got != tc.want {
			t.Errorf("provider %q: EmbeddingModel()=%q want %q", tc.provider, got, tc.want)
		}
	}
}
