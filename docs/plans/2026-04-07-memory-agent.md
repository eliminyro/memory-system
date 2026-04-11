# Memory Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go binary (`memory-agent`) that automatically captures session learnings via an adversarial two-agent pipeline (Extractor + Reviewer) and stores them in memory-mcp.

**Architecture:** Standalone Go binary triggered by Claude Code's Stop hook. Reads session JSONL transcripts, runs two sequential Claude API calls (Haiku by default) — one extracts candidate learnings, the other challenges each candidate against the existing KB. Survivors get stored/merged via memory-mcp's HTTP API. Config-driven auth (API key or subscription OAuth), model selection, and secret resolution.

**Tech Stack:** Go, `github.com/anthropics/anthropic-sdk-go`, `gopkg.in/yaml.v3`, MCP JSON-RPC over HTTP

**Spec:** `docs/specs/2026-04-07-knowledge-compilation-design.md` — Section 2

---

## File Structure

```
~/mystuff/goprojects/memory-agent/
├── cmd/
│   └── memory-agent/
│       └── main.go                 # CLI entry point, subcommands
├── internal/
│   ├── config/
│   │   ├── config.go               # YAML config parsing
│   │   └── config_test.go
│   ├── secret/
│   │   ├── resolve.go              # file://, gcp://, env://, literal:// resolution
│   │   └── resolve_test.go
│   ├── transcript/
│   │   ├── parser.go               # Parse Claude Code session JSONL
│   │   └── parser_test.go
│   ├── claude/
│   │   ├── client.go               # Anthropic API client (both auth modes)
│   │   └── client_test.go
│   ├── mcpclient/
│   │   ├── client.go               # memory-mcp MCP-over-HTTP client
│   │   └── client_test.go
│   ├── pipeline/
│   │   ├── types.go                # Candidate, Verdict types
│   │   ├── extractor.go            # Agent 1: extract candidates
│   │   ├── reviewer.go             # Agent 2: challenge candidates
│   │   ├── writer.go               # Store/merge accepted candidates
│   │   └── pipeline_test.go
│   └── state/
│       ├── state.go                # Dedup: last-capture.json
│       └── state_test.go
├── go.mod
└── go.sum
```

---

### Task 1: Project Scaffold + Config

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/go.mod`
- Create: `~/mystuff/goprojects/memory-agent/internal/config/config.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/config/config_test.go`

- [ ] **Step 1: Initialize Go module**

```bash
mkdir -p ~/mystuff/goprojects/memory-agent
cd ~/mystuff/goprojects/memory-agent
git init
go mod init github.com/eliminyro/memory-agent
```

- [ ] **Step 2: Write the failing test for config parsing**

```go
// internal/config/config_test.go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-agent/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(""), 0600)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "api", cfg.Auth)
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.Model)
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ExtractorModel())
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ReviewerModel())
}

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
auth: sdk
model: claude-haiku-4-5-20251001
extractor_model: claude-sonnet-4-5-20250929
reviewer_model: claude-haiku-4-5-20251001
memory_mcp_url: https://memory.example.com/mcp
memory_mcp_api_key: literal://test-key-123
`
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "sdk", cfg.Auth)
	assert.Equal(t, "claude-sonnet-4-5-20250929", cfg.ExtractorModel())
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.ReviewerModel())
	assert.Equal(t, "https://memory.example.com/mcp", cfg.MemoryMCPURL)
	assert.Equal(t, "literal://test-key-123", cfg.MemoryMCPAPIKey)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/config/ -v`
Expected: Compilation error — package doesn't exist yet.

- [ ] **Step 4: Implement config**

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Auth            string `yaml:"auth"`              // "api" or "sdk"
	APIKey          string `yaml:"api_key"`           // only when auth=api
	Model           string `yaml:"model"`             // default model for both agents
	ExtractorModelV string `yaml:"extractor_model"`   // override for extractor
	ReviewerModelV  string `yaml:"reviewer_model"`    // override for reviewer
	MemoryMCPURL    string `yaml:"memory_mcp_url"`    // memory-mcp endpoint
	MemoryMCPAPIKey string `yaml:"memory_mcp_api_key"` // secret source URI
}

func (c *Config) ExtractorModel() string {
	if c.ExtractorModelV != "" {
		return c.ExtractorModelV
	}
	return c.Model
}

func (c *Config) ReviewerModel() string {
	if c.ReviewerModelV != "" {
		return c.ReviewerModelV
	}
	return c.Model
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Auth:         "api",
		Model:        "claude-haiku-4-5-20251001",
		MemoryMCPURL: "https://memory-mcp.a11s.dev/mcp",
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Model == "" {
		cfg.Model = "claude-haiku-4-5-20251001"
	}
	if cfg.Auth == "" {
		cfg.Auth = "api"
	}

	return cfg, nil
}
```

- [ ] **Step 5: Install deps and run tests**

```bash
cd ~/mystuff/goprojects/memory-agent
go get gopkg.in/yaml.v3
go get github.com/stretchr/testify
go test ./internal/config/ -v
```
Expected: PASS

- [ ] **Step 6: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 2: Secret Resolution

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/secret/resolve.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/secret/resolve_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/secret/resolve_test.go
package secret_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-agent/internal/secret"
)

func TestResolve_Literal(t *testing.T) {
	val, err := secret.Resolve(context.Background(), "literal://my-secret-value")
	require.NoError(t, err)
	assert.Equal(t, "my-secret-value", val)
}

func TestResolve_Env(t *testing.T) {
	os.Setenv("TEST_SECRET_KEY", "env-secret-value")
	t.Cleanup(func() { os.Unsetenv("TEST_SECRET_KEY") })

	val, err := secret.Resolve(context.Background(), "env://TEST_SECRET_KEY")
	require.NoError(t, err)
	assert.Equal(t, "env-secret-value", val)
}

func TestResolve_Env_NotSet(t *testing.T) {
	_, err := secret.Resolve(context.Background(), "env://NONEXISTENT_VAR_12345")
	assert.Error(t, err)
}

func TestResolve_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	os.WriteFile(path, []byte("file-secret-value\n"), 0600)

	val, err := secret.Resolve(context.Background(), "file://"+path)
	require.NoError(t, err)
	assert.Equal(t, "file-secret-value", val)
}

func TestResolve_UnknownScheme(t *testing.T) {
	_, err := secret.Resolve(context.Background(), "ftp://something")
	assert.Error(t, err)
}

func TestResolve_PlainString(t *testing.T) {
	// No scheme prefix — treat as literal for backwards compat
	val, err := secret.Resolve(context.Background(), "plain-api-key")
	require.NoError(t, err)
	assert.Equal(t, "plain-api-key", val)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/secret/ -v`
Expected: Compilation error

- [ ] **Step 3: Implement secret resolution**

```go
// internal/secret/resolve.go
package secret

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Resolve resolves a secret URI to its plaintext value.
// Supported schemes: file://, env://, literal://, gcp://
// Strings without a scheme prefix are treated as literal values.
func Resolve(ctx context.Context, uri string) (string, error) {
	switch {
	case strings.HasPrefix(uri, "literal://"):
		return strings.TrimPrefix(uri, "literal://"), nil

	case strings.HasPrefix(uri, "env://"):
		name := strings.TrimPrefix(uri, "env://")
		val, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %q not set", name)
		}
		return val, nil

	case strings.HasPrefix(uri, "file://"):
		path := strings.TrimPrefix(uri, "file://")
		// Expand ~ to home dir
		if strings.HasPrefix(path, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("expand home dir: %w", err)
			}
			path = home + path[1:]
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read secret file %q: %w", path, err)
		}
		return strings.TrimSpace(string(data)), nil

	case strings.HasPrefix(uri, "gcp://"):
		return resolveGCP(ctx, uri)

	default:
		// No scheme — treat as literal value
		return uri, nil
	}
}

func resolveGCP(ctx context.Context, uri string) (string, error) {
	// gcp://project/secret-name or gcp://project/secret-name:version
	path := strings.TrimPrefix(uri, "gcp://")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid GCP secret URI %q: expected gcp://project/secret", uri)
	}
	project := parts[0]
	secretAndVersion := parts[1]

	secretName := secretAndVersion
	version := "latest"
	if idx := strings.LastIndex(secretAndVersion, ":"); idx != -1 {
		secretName = secretAndVersion[:idx]
		version = secretAndVersion[idx+1:]
	}

	// Use Google Secret Manager API via ADC
	// Import: cloud.google.com/go/secretmanager/apiv1 + secretmanagerpb
	// For now, shell out to gcloud to keep deps light. Replace with library if needed.
	_ = ctx
	_ = project
	_ = secretName
	_ = version
	return "", fmt.Errorf("GCP secret resolution not yet implemented (would resolve %s/%s:%s)", project, secretName, version)
}
```

- [ ] **Step 4: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/secret/ -v`
Expected: PASS (GCP test not included — it's a stub)

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 3: Session Transcript Parser

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/transcript/parser.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/transcript/parser_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/transcript/parser_test.go
package transcript_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-agent/internal/transcript"
)

func TestParse_BasicSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	// Minimal Claude Code session JSONL — each line is a JSON object
	lines := []string{
		`{"type":"human","message":{"role":"user","content":"How do I use GORM associations?"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"GORM supports has-one, has-many, belongs-to, and many-to-many associations. Here's how..."}}`,
		`{"type":"human","message":{"role":"user","content":"What about preloading?"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"Use db.Preload(\"Items\").Find(&orders) to eager-load associations..."}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(path, []byte(content), 0600)

	session, err := transcript.Parse(path)
	require.NoError(t, err)
	assert.Equal(t, 4, len(session.Messages))
	assert.Contains(t, session.Summary(), "GORM")
}

func TestParse_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(path, []byte(""), 0600)

	session, err := transcript.Parse(path)
	require.NoError(t, err)
	assert.Equal(t, 0, len(session.Messages))
}

func TestParse_SkipToolCalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []string{
		`{"type":"human","message":{"role":"user","content":"Read the file"}}`,
		`{"type":"tool_use","tool":"Read","input":{"path":"/tmp/foo.go"}}`,
		`{"type":"tool_result","output":"package main..."}`,
		`{"type":"assistant","message":{"role":"assistant","content":"The file contains a Go package..."}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(path, []byte(content), 0600)

	session, err := transcript.Parse(path)
	require.NoError(t, err)
	// Summary should only include human/assistant text, not tool I/O
	summary := session.Summary()
	assert.Contains(t, summary, "Read the file")
	assert.Contains(t, summary, "Go package")
	assert.NotContains(t, summary, "package main")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/transcript/ -v`
Expected: Compilation error

- [ ] **Step 3: Implement transcript parser**

```go
// internal/transcript/parser.go
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Message represents a single human or assistant message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Session represents a parsed Claude Code session transcript.
type Session struct {
	Messages []Message
	Path     string
}

// jsonlEntry is the raw JSONL structure from Claude Code session logs.
type jsonlEntry struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string `json:"role"`
		Content any    `json:"content"` // can be string or []block
	} `json:"message,omitempty"`
}

// Parse reads a Claude Code session JSONL file and extracts human/assistant messages.
func Parse(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	session := &Session{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry jsonlEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		if entry.Type != "human" && entry.Type != "assistant" {
			continue
		}
		if entry.Message == nil {
			continue
		}

		content := extractContent(entry.Message.Content)
		if content == "" {
			continue
		}

		session.Messages = append(session.Messages, Message{
			Role:    entry.Message.Role,
			Content: content,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	return session, nil
}

// extractContent handles both string content and structured content blocks.
func extractContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", raw)
	}
}

// Summary returns a concatenated text of all messages for LLM consumption.
// Prefixes each message with its role for context.
func (s *Session) Summary() string {
	var b strings.Builder
	for _, m := range s.Messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/transcript/ -v`
Expected: PASS

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 4: Claude API Client Wrapper

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/claude/client.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/claude/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/claude/client_test.go
package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/eliminyro/memory-agent/internal/claude"
)

func TestNewClient_APIKey(t *testing.T) {
	c, err := claude.NewClient("api", "sk-test-key-123")
	assert.NoError(t, err)
	assert.NotNil(t, c)
}

func TestNewClient_SDK_NoCredentials(t *testing.T) {
	// SDK mode with non-existent credentials file should error
	c, err := claude.NewClient("sdk", "")
	// May or may not error depending on whether ~/.claude/.credentials.json exists
	// Just verify it doesn't panic
	_ = c
	_ = err
}

func TestNewClient_InvalidAuth(t *testing.T) {
	_, err := claude.NewClient("invalid", "")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/claude/ -v`
Expected: Compilation error

- [ ] **Step 3: Install Anthropic SDK and implement client**

```bash
cd ~/mystuff/goprojects/memory-agent
go get github.com/anthropics/anthropic-sdk-go
```

```go
// internal/claude/client.go
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Client wraps the Anthropic SDK client.
type Client struct {
	inner *anthropic.Client
}

// NewClient creates a Claude API client.
// auth: "api" uses the provided apiKey (or ANTHROPIC_API_KEY env).
// auth: "sdk" reads OAuth token from ~/.claude/.credentials.json (subscription).
func NewClient(auth, apiKey string) (*Client, error) {
	var opts []option.RequestOption

	switch auth {
	case "api":
		if apiKey != "" {
			opts = append(opts, option.WithAPIKey(apiKey))
		}
		// else: falls back to ANTHROPIC_API_KEY env var (SDK default)
	case "sdk":
		token, err := readSubscriptionToken()
		if err != nil {
			return nil, fmt.Errorf("read subscription token: %w", err)
		}
		opts = append(opts, option.WithAuthToken(token))
	default:
		return nil, fmt.Errorf("unsupported auth mode: %q (use \"api\" or \"sdk\")", auth)
	}

	return &Client{inner: anthropic.NewClient(opts...)}, nil
}

// Complete sends a message to Claude and returns the text response.
func (c *Client) Complete(ctx context.Context, model, systemPrompt, userMessage string) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 8192,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMessage)),
		},
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	msg, err := c.inner.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("claude API call: %w", err)
	}

	if len(msg.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	return msg.Content[0].Text, nil
}

// credentialsFile is the Claude Code OAuth credentials store.
type credentialsFile struct {
	ClaudeAIOAuth *struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

func readSubscriptionToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	var creds credentialsFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if creds.ClaudeAIOAuth == nil || creds.ClaudeAIOAuth.AccessToken == "" {
		return "", fmt.Errorf("no OAuth token found in credentials")
	}

	return creds.ClaudeAIOAuth.AccessToken, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/claude/ -v`
Expected: PASS

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 5: Memory-MCP HTTP Client

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/mcpclient/client.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/mcpclient/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/mcpclient/client_test.go
package mcpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-agent/internal/mcpclient"
)

func TestClient_SearchMemory(t *testing.T) {
	// Mock MCP server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "tools/call", req["method"])

		params := req["params"].(map[string]any)
		assert.Equal(t, "search_memory", params["name"])

		// Return mock MCP response
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `[{"section_id":"abc","content":"existing knowledge","score":0.85}]`},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := mcpclient.New(srv.URL, "test-key")
	results, err := client.SearchMemory(context.Background(), "test query", nil, nil, 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Content, "existing knowledge")
}

func TestClient_StoreMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		params := req["params"].(map[string]any)
		assert.Equal(t, "store_memory", params["name"])

		args := params["arguments"].(map[string]any)
		assert.Equal(t, "learnings", args["category"])
		assert.Equal(t, "gorm", args["slug"])

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `{"id":"doc-123","path":"learnings/go/gorm","sections":2}`},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := mcpclient.New(srv.URL, "test-key")
	sub := "go"
	err := client.StoreMemory(context.Background(), "learnings", &sub, "gorm", "# GORM\n\n## Patterns\n\nContent here")
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/mcpclient/ -v`
Expected: Compilation error

- [ ] **Step 3: Implement MCP client**

```go
// internal/mcpclient/client.go
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// Client communicates with memory-mcp over MCP-over-HTTP (JSON-RPC 2.0).
type Client struct {
	url    string
	apiKey string
	http   *http.Client
	nextID atomic.Int64
}

func New(url, apiKey string) *Client {
	return &Client{
		url:    url,
		apiKey: apiKey,
		http:   &http.Client{},
	}
}

// SearchResult matches memory-mcp's search_memory response.
type SearchResult struct {
	SectionID string  `json:"section_id"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
	Category  string  `json:"category"`
	DocTitle  string  `json:"doc_title"`
}

// SearchMemory calls the search_memory MCP tool.
func (c *Client) SearchMemory(ctx context.Context, query string, category, subcategory *string, limit int) ([]SearchResult, error) {
	args := map[string]any{"query": query, "limit": limit}
	if category != nil {
		args["category"] = *category
	}
	if subcategory != nil {
		args["subcategory"] = *subcategory
	}

	text, err := c.callTool(ctx, "search_memory", args)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return results, nil
}

// StoreMemory calls the store_memory MCP tool.
func (c *Client) StoreMemory(ctx context.Context, category string, subcategory *string, slug, content string) error {
	args := map[string]any{
		"category": category,
		"slug":     slug,
		"content":  content,
	}
	if subcategory != nil {
		args["subcategory"] = *subcategory
	}

	_, err := c.callTool(ctx, "store_memory", args)
	return err
}

// UpdateSection calls the update_section MCP tool.
func (c *Client) UpdateSection(ctx context.Context, sectionID, content string) error {
	args := map[string]any{
		"section_id": sectionID,
		"content":    content,
	}
	_, err := c.callTool(ctx, "update_section", args)
	return err
}

// callTool sends a JSON-RPC tools/call request and returns the text content.
func (c *Client) callTool(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
	id := c.nextID.Add(1)

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": arguments,
		},
		"id": id,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("MCP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MCP error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var rpcResp struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("parse MCP response: %w", err)
	}
	if rpcResp.Error != nil {
		return "", fmt.Errorf("MCP tool error: %s", rpcResp.Error.Message)
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Content) == 0 {
		return "", fmt.Errorf("empty MCP response")
	}
	if rpcResp.Result.IsError {
		return "", fmt.Errorf("MCP tool returned error: %s", rpcResp.Result.Content[0].Text)
	}

	return rpcResp.Result.Content[0].Text, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/mcpclient/ -v`
Expected: PASS

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 6: Pipeline Types + Extractor Agent

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/pipeline/types.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/pipeline/extractor.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/pipeline/pipeline_test.go`

- [ ] **Step 1: Define pipeline types**

```go
// internal/pipeline/types.go
package pipeline

// Candidate represents a knowledge item extracted from a session.
type Candidate struct {
	Path    string `json:"path"`    // category/subcategory/slug
	Heading string `json:"heading"` // section heading
	Content string `json:"content"` // the actual knowledge
	Type    string `json:"type"`    // "new" or "update"
}

// Verdict is the reviewer's decision on a candidate.
type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictMerge  Verdict = "merge"
	VerdictReject Verdict = "reject"
)

// ReviewedCandidate is a candidate with its review verdict.
type ReviewedCandidate struct {
	Candidate
	Verdict  Verdict `json:"verdict"`
	Reason   string  `json:"reason"`
	MergeTarget string `json:"merge_target,omitempty"` // section_id for merge verdicts
}

// ParsePath splits "category/subcategory/slug" into parts.
func ParsePath(path string) (category string, subcategory *string, slug string) {
	parts := splitPath(path)
	switch len(parts) {
	case 3:
		return parts[0], &parts[1], parts[2]
	case 2:
		return parts[0], nil, parts[1]
	default:
		return "misc", nil, path
	}
}

func splitPath(path string) []string {
	var parts []string
	current := ""
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
```

- [ ] **Step 2: Write the failing test for extractor**

```go
// internal/pipeline/pipeline_test.go
package pipeline_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/eliminyro/memory-agent/internal/pipeline"
)

func TestParsePath_ThreeParts(t *testing.T) {
	cat, sub, slug := pipeline.ParsePath("learnings/go/gorm")
	assert.Equal(t, "learnings", cat)
	assert.NotNil(t, sub)
	assert.Equal(t, "go", *sub)
	assert.Equal(t, "gorm", slug)
}

func TestParsePath_TwoParts(t *testing.T) {
	cat, sub, slug := pipeline.ParsePath("preferences/style")
	assert.Equal(t, "preferences", cat)
	assert.Nil(t, sub)
	assert.Equal(t, "style", slug)
}

func TestParseExtractorResponse(t *testing.T) {
	raw := `[
		{"path": "learnings/go/gorm", "heading": "GORM Associations", "content": "Use Preload for eager loading", "type": "new"},
		{"path": "learnings/infrastructure/k8s", "heading": "K8s Networking", "content": "Services expose pods", "type": "update"}
	]`
	candidates, err := pipeline.ParseExtractorResponse(raw)
	assert.NoError(t, err)
	assert.Len(t, candidates, 2)
	assert.Equal(t, "learnings/go/gorm", candidates[0].Path)
	assert.Equal(t, "new", candidates[0].Type)
}

func TestParseExtractorResponse_Empty(t *testing.T) {
	candidates, err := pipeline.ParseExtractorResponse("[]")
	assert.NoError(t, err)
	assert.Len(t, candidates, 0)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/pipeline/ -v`
Expected: Compilation error — `ParseExtractorResponse` doesn't exist.

- [ ] **Step 4: Implement extractor**

```go
// internal/pipeline/extractor.go
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eliminyro/memory-agent/internal/claude"
	"github.com/eliminyro/memory-agent/internal/transcript"
)

const extractorSystemPrompt = `You are a knowledge extraction agent. Your job is to identify durable, reusable learnings from a Claude Code session transcript.

For each learning, output a JSON object with:
- "path": where it belongs in the knowledge hierarchy (category/subcategory/slug format)
  Categories: learnings, preferences, projects
  Subcategories for learnings: go, infrastructure, cicd, observability, tools, homelab
- "heading": a descriptive heading for the knowledge
- "content": the actual knowledge, written as a durable fact (NOT "we discussed X" but "X works by doing Y")
- "type": "new" (create new doc) or "update" (merge into existing doc)

Rules:
- Extract 0-7 candidates per session. Zero is valid — not every session produces learnings.
- Only extract durable knowledge that would be useful months from now.
- Skip ephemeral conversation, debugging steps, and session-specific details.
- Write content as standalone facts, not as references to the conversation.
- Use clear, concise language.

Respond with ONLY a JSON array of candidates. No other text.`

// Extract runs Agent 1 (Extractor) on a session transcript.
func Extract(ctx context.Context, client *claude.Client, model string, session *transcript.Session) ([]Candidate, error) {
	if len(session.Messages) == 0 {
		return nil, nil
	}

	summary := session.Summary()

	// Truncate if too long (Haiku context is smaller)
	if len(summary) > 100_000 {
		summary = summary[:100_000] + "\n\n[TRUNCATED]"
	}

	response, err := client.Complete(ctx, model, extractorSystemPrompt, summary)
	if err != nil {
		return nil, fmt.Errorf("extractor: %w", err)
	}

	return ParseExtractorResponse(response)
}

// ParseExtractorResponse parses the JSON array from the extractor's response.
func ParseExtractorResponse(raw string) ([]Candidate, error) {
	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var candidates []Candidate
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return nil, fmt.Errorf("parse extractor response: %w (raw: %.200s)", err, raw)
	}
	return candidates, nil
}
```

- [ ] **Step 5: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/pipeline/ -v`
Expected: PASS

- [ ] **Step 6: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 7: Reviewer Agent

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/pipeline/reviewer.go`
- Modify: `~/mystuff/goprojects/memory-agent/internal/pipeline/pipeline_test.go`

- [ ] **Step 1: Add test for reviewer response parsing**

Add to `pipeline_test.go`:

```go
func TestParseReviewerResponse(t *testing.T) {
	raw := `[
		{"path": "learnings/go/gorm", "heading": "GORM Associations", "content": "Use Preload for eager loading", "type": "new", "verdict": "accept", "reason": "new topic not in KB"},
		{"path": "learnings/infrastructure/k8s", "heading": "K8s Networking", "content": "Services expose pods", "type": "update", "verdict": "reject", "reason": "already exists with same content"}
	]`
	reviewed, err := pipeline.ParseReviewerResponse(raw)
	assert.NoError(t, err)
	assert.Len(t, reviewed, 2)
	assert.Equal(t, pipeline.VerdictAccept, reviewed[0].Verdict)
	assert.Equal(t, pipeline.VerdictReject, reviewed[1].Verdict)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/pipeline/ -run TestParseReviewer -v`
Expected: Compilation error

- [ ] **Step 3: Implement reviewer**

```go
// internal/pipeline/reviewer.go
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eliminyro/memory-agent/internal/claude"
	"github.com/eliminyro/memory-agent/internal/mcpclient"
)

const reviewerSystemPrompt = `You are a knowledge review agent. Your job is to challenge candidate learnings extracted from a Claude Code session.

For each candidate, you are given:
- The candidate itself (path, heading, content, type)
- Search results from the existing knowledge base (if any)

For each candidate, decide:
- "accept": new knowledge not in KB, worth storing
- "merge": overlaps with existing doc — should update existing section instead of creating new
- "reject": duplicate, too ephemeral, too specific to session, or not worth storing

Criteria:
- Is this durable knowledge? Would it be useful 3 months from now?
- Is it already in the KB? (check search results for high similarity)
- Is it too specific to this one session?
- Is it a complete thought or a fragment?

For "merge" verdicts, include the section_id of the target section in "merge_target".

Respond with ONLY a JSON array of reviewed candidates. Each object has the original fields plus "verdict", "reason", and optionally "merge_target".`

// Review runs Agent 2 (Reviewer) on extracted candidates.
func Review(ctx context.Context, client *claude.Client, model string, mcp *mcpclient.Client, candidates []Candidate) ([]ReviewedCandidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	// For each candidate, search the KB for similar content
	type candidateWithContext struct {
		Candidate     Candidate             `json:"candidate"`
		SearchResults []mcpclient.SearchResult `json:"existing_knowledge"`
	}

	var enriched []candidateWithContext
	for _, c := range candidates {
		results, err := mcp.SearchMemory(ctx, c.Content, nil, nil, 3)
		if err != nil {
			// Non-fatal: reviewer can work without search results
			results = nil
		}
		enriched = append(enriched, candidateWithContext{
			Candidate:     c,
			SearchResults: results,
		})
	}

	input, err := json.MarshalIndent(enriched, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal reviewer input: %w", err)
	}

	response, err := client.Complete(ctx, model, reviewerSystemPrompt, string(input))
	if err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}

	return ParseReviewerResponse(response)
}

// ParseReviewerResponse parses the JSON array from the reviewer's response.
func ParseReviewerResponse(raw string) ([]ReviewedCandidate, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var reviewed []ReviewedCandidate
	if err := json.Unmarshal([]byte(raw), &reviewed); err != nil {
		return nil, fmt.Errorf("parse reviewer response: %w (raw: %.200s)", err, raw)
	}
	return reviewed, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/pipeline/ -v`
Expected: PASS

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 8: Writer + State Management

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/internal/pipeline/writer.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/state/state.go`
- Create: `~/mystuff/goprojects/memory-agent/internal/state/state_test.go`

- [ ] **Step 1: Write the failing test for state**

```go
// internal/state/state_test.go
package state_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-agent/internal/state"
)

func TestState_AlreadyCaptured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-capture.json")

	s := state.New(path)

	assert.False(t, s.AlreadyCaptured("session-abc"))

	require.NoError(t, s.MarkCaptured("session-abc"))
	assert.True(t, s.AlreadyCaptured("session-abc"))

	// Different session
	assert.False(t, s.AlreadyCaptured("session-xyz"))
}

func TestState_PersistAcrossLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-capture.json")

	s1 := state.New(path)
	require.NoError(t, s1.MarkCaptured("session-abc"))

	s2 := state.New(path)
	assert.True(t, s2.AlreadyCaptured("session-abc"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/state/ -v`
Expected: Compilation error

- [ ] **Step 3: Implement state**

```go
// internal/state/state.go
package state

import (
	"encoding/json"
	"os"
	"time"
)

// State tracks which sessions have been captured.
type State struct {
	path string
	data stateData
}

type stateData struct {
	Sessions map[string]time.Time `json:"sessions"`
}

func New(path string) *State {
	s := &State{
		path: path,
		data: stateData{Sessions: make(map[string]time.Time)},
	}
	s.load()
	return s
}

func (s *State) AlreadyCaptured(sessionID string) bool {
	_, ok := s.data.Sessions[sessionID]
	return ok
}

func (s *State) MarkCaptured(sessionID string) error {
	s.data.Sessions[sessionID] = time.Now()
	return s.save()
}

func (s *State) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &s.data)
	if s.data.Sessions == nil {
		s.data.Sessions = make(map[string]time.Time)
	}
}

func (s *State) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}
```

- [ ] **Step 4: Run state tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./internal/state/ -v`
Expected: PASS

- [ ] **Step 5: Implement writer**

```go
// internal/pipeline/writer.go
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/eliminyro/memory-agent/internal/mcpclient"
)

// Write stores accepted candidates and merges merge-candidates into memory-mcp.
// Returns counts of accepted, merged, and rejected candidates.
func Write(ctx context.Context, mcp *mcpclient.Client, reviewed []ReviewedCandidate) (accepted, merged, rejected int, err error) {
	for _, rc := range reviewed {
		switch rc.Verdict {
		case VerdictAccept:
			category, subcategory, slug := ParsePath(rc.Path)
			content := formatContent(rc.Heading, rc.Content)
			if err := mcp.StoreMemory(ctx, category, subcategory, slug, content); err != nil {
				slog.Error("failed to store candidate", "path", rc.Path, "error", err)
				continue
			}
			slog.Info("stored new knowledge", "path", rc.Path, "heading", rc.Heading)
			accepted++

		case VerdictMerge:
			if rc.MergeTarget == "" {
				slog.Warn("merge verdict without target, storing as new", "path", rc.Path)
				category, subcategory, slug := ParsePath(rc.Path)
				content := formatContent(rc.Heading, rc.Content)
				if err := mcp.StoreMemory(ctx, category, subcategory, slug, content); err != nil {
					slog.Error("failed to store merged candidate", "path", rc.Path, "error", err)
					continue
				}
				accepted++
				continue
			}
			if err := mcp.UpdateSection(ctx, rc.MergeTarget, rc.Content); err != nil {
				slog.Error("failed to merge candidate", "path", rc.Path, "target", rc.MergeTarget, "error", err)
				continue
			}
			slog.Info("merged knowledge", "path", rc.Path, "target", rc.MergeTarget)
			merged++

		case VerdictReject:
			slog.Info("rejected candidate", "path", rc.Path, "reason", rc.Reason)
			rejected++
		}
	}
	return accepted, merged, rejected, nil
}

func formatContent(heading, content string) string {
	var b strings.Builder
	if heading != "" {
		b.WriteString("# ")
		b.WriteString(heading)
		b.WriteString("\n\n")
	}
	b.WriteString(content)
	return b.String()
}
```

- [ ] **Step 6: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-agent && go build ./...`
Expected: PASS

- [ ] **Step 7: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`

---

### Task 9: Main Command + Capture Subcommand

**Files:**
- Create: `~/mystuff/goprojects/memory-agent/cmd/memory-agent/main.go`

- [ ] **Step 1: Implement main entry point**

```go
// cmd/memory-agent/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/eliminyro/memory-agent/internal/claude"
	"github.com/eliminyro/memory-agent/internal/config"
	"github.com/eliminyro/memory-agent/internal/mcpclient"
	"github.com/eliminyro/memory-agent/internal/pipeline"
	"github.com/eliminyro/memory-agent/internal/secret"
	"github.com/eliminyro/memory-agent/internal/state"
	"github.com/eliminyro/memory-agent/internal/transcript"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: memory-agent <command> [args]\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  capture <session.jsonl>  Extract and store learnings from a session\n")
		os.Exit(1)
	}

	// Recursion guard: if spawned by memory-agent (via hook), don't re-capture
	if os.Getenv("MEMORY_AGENT_INVOKED") == "1" {
		slog.Info("recursion guard: skipping (MEMORY_AGENT_INVOKED=1)")
		return
	}
	os.Setenv("MEMORY_AGENT_INVOKED", "1")

	switch os.Args[1] {
	case "capture":
		if err := runCapture(os.Args[2:]); err != nil {
			slog.Error("capture failed", "error", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "Path to config file")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: memory-agent capture <session.jsonl>")
	}
	sessionPath := fs.Arg(0)

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()

	// Check dedup state
	st := state.New(stateFilePath())
	sessionID := filepath.Base(sessionPath)
	if st.AlreadyCaptured(sessionID) {
		slog.Info("session already captured, skipping", "session", sessionID)
		return nil
	}

	// Parse transcript
	session, err := transcript.Parse(sessionPath)
	if err != nil {
		return fmt.Errorf("parse transcript: %w", err)
	}
	if len(session.Messages) == 0 {
		slog.Info("empty session, skipping")
		return nil
	}

	// Create Claude client
	apiKey := cfg.APIKey
	if cfg.Auth == "api" && apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	claudeClient, err := claude.NewClient(cfg.Auth, apiKey)
	if err != nil {
		return fmt.Errorf("create claude client: %w", err)
	}

	// Create memory-mcp client
	mcpAPIKey, err := secret.Resolve(ctx, cfg.MemoryMCPAPIKey)
	if err != nil {
		return fmt.Errorf("resolve MCP API key: %w", err)
	}
	mcpClient := mcpclient.New(cfg.MemoryMCPURL, mcpAPIKey)

	// Agent 1: Extract candidates
	slog.Info("running extractor", "model", cfg.ExtractorModel(), "messages", len(session.Messages))
	candidates, err := pipeline.Extract(ctx, claudeClient, cfg.ExtractorModel(), session)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	slog.Info("extracted candidates", "count", len(candidates))
	if len(candidates) == 0 {
		st.MarkCaptured(sessionID)
		return nil
	}

	// Agent 2: Review candidates
	slog.Info("running reviewer", "model", cfg.ReviewerModel(), "candidates", len(candidates))
	reviewed, err := pipeline.Review(ctx, claudeClient, cfg.ReviewerModel(), mcpClient, candidates)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	// Write accepted/merged candidates
	accepted, merged, rejected, err := pipeline.Write(ctx, mcpClient, reviewed)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	slog.Info("capture complete", "accepted", accepted, "merged", merged, "rejected", rejected)

	// Mark session as captured
	st.MarkCaptured(sessionID)
	return nil
}

func defaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "memory-capture.yaml")
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "state", "memory-capture")
	os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "last-capture.json")
}
```

- [ ] **Step 2: Build the binary**

Run: `cd ~/mystuff/goprojects/memory-agent && go build -o memory-agent ./cmd/memory-agent/`
Expected: Binary created at `~/mystuff/goprojects/memory-agent/memory-agent`

- [ ] **Step 3: Test help output**

Run: `cd ~/mystuff/goprojects/memory-agent && ./memory-agent`
Expected: Usage message printed to stderr, exit code 1

- [ ] **Step 4: Run all tests**

Run: `cd ~/mystuff/goprojects/memory-agent && go test ./... -v`
Expected: All PASS

- [ ] **Step 5: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w . && go vet ./...`
Expected: Clean

---

### Task 10: GCP Secret Manager Implementation

**Files:**
- Modify: `~/mystuff/goprojects/memory-agent/internal/secret/resolve.go`
- Modify: `~/mystuff/goprojects/memory-agent/internal/secret/resolve_test.go`

- [ ] **Step 1: Install GCP Secret Manager dependency**

```bash
cd ~/mystuff/goprojects/memory-agent
go get cloud.google.com/go/secretmanager
go get google.golang.org/api
```

- [ ] **Step 2: Replace the GCP stub in resolve.go**

Replace the `resolveGCP` function:

```go
import (
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

func resolveGCP(ctx context.Context, uri string) (string, error) {
	path := strings.TrimPrefix(uri, "gcp://")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid GCP secret URI %q: expected gcp://project/secret", uri)
	}
	project := parts[0]
	secretAndVersion := parts[1]

	secretName := secretAndVersion
	version := "latest"
	if idx := strings.LastIndex(secretAndVersion, ":"); idx != -1 {
		secretName = secretAndVersion[:idx]
		version = secretAndVersion[idx+1:]
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("create secret manager client: %w", err)
	}
	defer client.Close()

	name := fmt.Sprintf("projects/%s/secrets/%s/versions/%s", project, secretName, version)
	result, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("access secret %s: %w", name, err)
	}

	return string(result.Payload.Data), nil
}
```

- [ ] **Step 3: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-agent && go build ./...`
Expected: PASS

- [ ] **Step 4: Format**

Run: `cd ~/mystuff/goprojects/memory-agent && gofmt -w .`
