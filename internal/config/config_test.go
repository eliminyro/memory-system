package config

import (
	"os"
	"strings"
	"testing"
)

func TestParseTenantDefaults(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    TenantDefaults
		wantErr string
	}{
		{
			name:  "empty string yields safe baseline",
			input: "",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "all three set",
			input: "staleness=hard,duplicate_guard=true,cleanup_scan_enabled=true",
			want:  TenantDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true},
		},
		{
			name:  "partial: just staleness",
			input: "staleness=advisory",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "partial: just duplicate_guard",
			input: "duplicate_guard=true",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "whitespace tolerated around tokens",
			input: "  staleness = advisory ,  duplicate_guard = true ",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "staleness value is case-insensitive",
			input: "staleness=ADVISORY",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "bool accepts true/false case-insensitive",
			input: "duplicate_guard=TRUE,cleanup_scan_enabled=False",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "bool accepts 1 and 0",
			input: "duplicate_guard=1,cleanup_scan_enabled=0",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
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
		wantErr  string
	}{
		{name: "ollama default needs nothing", provider: "ollama"},
		{name: "openai with model ok", provider: "openai", openaiM: "text-embedding-3-small"},
		{name: "openai without model rejected", provider: "openai", wantErr: "OPENAI_EMBEDDING_MODEL is required"},
		{name: "aws with region+model ok", provider: "aws", awsReg: "us-east-1", awsM: "amazon.titan-embed-text-v2:0"},
		{name: "aws without region rejected", provider: "aws", awsM: "amazon.titan-embed-text-v2:0", wantErr: "AWS_REGION and AWS_EMBEDDING_MODEL"},
		{name: "aws without model rejected", provider: "aws", awsReg: "us-east-1", wantErr: "AWS_REGION and AWS_EMBEDDING_MODEL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EMBEDDING_PROVIDER", tc.provider)
			t.Setenv("OPENAI_EMBEDDING_MODEL", tc.openaiM)
			t.Setenv("AWS_REGION", tc.awsReg)
			t.Setenv("AWS_EMBEDDING_MODEL", tc.awsM)

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

func TestDefaultTenantDefaults(t *testing.T) {
	got := DefaultTenantDefaults()
	want := TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
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
