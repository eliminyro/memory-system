# Knowledge Compilation — Integration Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the knowledge compilation system together: hook engine injects KB index at session start, spawns memory-agent at session stop, `/ingest` skill enables interactive external source ingestion.

**Architecture:** Extend claude-hook-engine with index injection in session-start and a new session-stop subcommand that spawns `memory-agent capture` as a detached process. Add `/ingest` as a Claude Code skill file. Scheduled lint is a `/schedule` trigger config.

**Tech Stack:** Go (hook engine), Claude Code skills (markdown), memory-mcp HTTP API

**Spec:** `docs/specs/2026-04-07-knowledge-compilation-design.md` — Sections 3, 4, 5

**Repos:**
- Hook engine: `~/mystuff/goprojects/claude-hook-engine`
- Skills: `~/.claude/skills/ingest/`

---

## File Structure

```
# Hook engine changes
~/mystuff/goprojects/claude-hook-engine/
├── cmd/main.go                          # Add session-stop dispatch
├── internal/
│   ├── hook/
│   │   ├── session.go                   # Extend: index injection in session-start
│   │   ├── stop.go                      # NEW: session-stop handler (spawn memory-agent)
│   │   └── stop_test.go                 # NEW: tests
│   └── config/
│       └── config.go                    # Add MemoryMCP config + Stop section

# /ingest skill
~/.claude/skills/ingest/
└── ingest.md                            # Skill definition
```

---

### Task 1: Add MemoryMCP Config to Hook Engine

**Files:**
- Modify: `~/mystuff/goprojects/claude-hook-engine/internal/config/config.go`

- [ ] **Step 1: Add MemoryMCPConfig to Config struct**

Add to `internal/config/config.go`, after `ProjectConfig`:

```go
// MemoryMCPConfig holds connection details for the memory-mcp server.
type MemoryMCPConfig struct {
	URL    string `json:"url" yaml:"url"`       // e.g. https://memory-mcp.a11s.dev/mcp
	APIKey string `json:"api_key" yaml:"api_key"` // secret URI (file://, env://, literal://)
}
```

Add the field to `Config` struct:

```go
type Config struct {
	// ... existing fields ...
	MemoryMCP MemoryMCPConfig `json:"memory_mcp" yaml:"memory_mcp"`
}
```

- [ ] **Step 2: Add MemoryAgentConfig to Config struct**

```go
// MemoryAgentConfig holds the path to the memory-agent binary.
type MemoryAgentConfig struct {
	BinaryPath string `json:"binary_path" yaml:"binary_path"` // path to memory-agent binary
	ConfigPath string `json:"config_path" yaml:"config_path"` // path to memory-capture.yaml
}
```

Add to `Config`:

```go
MemoryAgent MemoryAgentConfig `json:"memory_agent" yaml:"memory_agent"`
```

- [ ] **Step 3: Add defaults in applyDefaults**

Add at the end of `applyDefaults`:

```go
// Memory MCP defaults
if cfg.MemoryMCP.URL == "" {
	cfg.MemoryMCP.URL = "https://memory-mcp.a11s.dev/mcp"
}

// Memory Agent defaults
if cfg.MemoryAgent.BinaryPath == "" {
	cfg.MemoryAgent.BinaryPath = "memory-agent" // assume on PATH
}
```

- [ ] **Step 4: Add rules.json config section**

Add to `~/.claude/hooks/rules.json` at the top level (alongside `pre`, `post`, `projects`):

```json
"memory_mcp": {
  "url": "https://memory-mcp.a11s.dev/mcp",
  "api_key": "file://~/.config/memory-agent/credentials"
},
"memory_agent": {
  "binary_path": "memory-agent",
  "config_path": "~/.claude/memory-capture.yaml"
}
```

- [ ] **Step 5: Verify compilation**

Run: `cd ~/mystuff/goprojects/claude-hook-engine && go build ./...`
Expected: PASS

---

### Task 2: Extend SessionStart — Index Injection

**Files:**
- Modify: `~/mystuff/goprojects/claude-hook-engine/internal/hook/session.go`

- [ ] **Step 1: Add index fetching function**

Add to `internal/hook/session.go`:

```go
import (
	"bytes"
	"io"
	"net/http"
	"time"
)

// fetchIndex calls memory-mcp's generate_index tool and returns the text result.
// Returns empty string on any error (non-fatal — session continues without index).
func fetchIndex(url, apiKey string) string {
	if url == "" || apiKey == "" {
		return ""
	}

	// Resolve API key if it's a file:// or env:// URI
	resolvedKey := resolveSimpleSecret(apiKey)
	if resolvedKey == "" {
		return ""
	}

	reqBody := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"generate_index","arguments":{"depth":"summary"}},"id":1}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(reqBody))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+resolvedKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB max
	if err != nil {
		return ""
	}

	// Parse JSON-RPC response to extract text content
	var rpcResp struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return ""
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Content) == 0 {
		return ""
	}

	return rpcResp.Result.Content[0].Text
}

// resolveSimpleSecret handles file:// and env:// URIs for the hook engine.
// Kept minimal — no GCP dependency in the hook engine.
func resolveSimpleSecret(uri string) string {
	switch {
	case strings.HasPrefix(uri, "file://"):
		path := strings.TrimPrefix(uri, "file://")
		if strings.HasPrefix(path, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			path = home + path[1:]
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	case strings.HasPrefix(uri, "env://"):
		return os.Getenv(strings.TrimPrefix(uri, "env://"))
	case strings.HasPrefix(uri, "literal://"):
		return strings.TrimPrefix(uri, "literal://")
	default:
		return uri
	}
}
```

- [ ] **Step 2: Modify HandleSessionStart to inject index**

Update `HandleSessionStart` to call `fetchIndex` and append the result to the context hint.

Replace the current function:

```go
func HandleSessionStart(r io.Reader, rulesPath string) (string, error) {
	var inp sessionInput
	if err := json.NewDecoder(r).Decode(&inp); err != nil {
		return "", fmt.Errorf("decoding session-start input: %w", err)
	}

	cfg, err := config.Load(rulesPath)
	if err != nil {
		return "", fmt.Errorf("session-start: %w", err)
	}

	var contextParts []string

	// Project detection (existing behavior)
	if len(cfg.Projects) > 0 {
		for name, proj := range cfg.Projects {
			for _, path := range proj.Paths {
				if matchesPath(inp.CWD, path) {
					contextParts = append(contextParts, buildProjectHint(name, proj))
					break
				}
			}
		}
	}

	// Knowledge base index injection
	if idx := fetchIndex(cfg.MemoryMCP.URL, cfg.MemoryMCP.APIKey); idx != "" {
		contextParts = append(contextParts, formatIndexHint(idx))
	}

	if len(contextParts) == 0 {
		return "", nil
	}

	hint := strings.Join(contextParts, "\n\n")
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": hint,
		},
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}
```

Rename `buildContextHint` to `buildProjectHint` and simplify it to return just the hint text (not the full JSON envelope):

```go
func buildProjectHint(name string, proj config.ProjectConfig) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Project detected: **%s**", name))
	if proj.Description != "" {
		parts = append(parts, proj.Description)
	}
	if proj.Memory != "" {
		memParts := strings.SplitN(proj.Memory, "/", 3)
		if len(memParts) == 3 {
			parts = append(parts, fmt.Sprintf(
				"Load project context: `mcp__memory__get_document(category=%q, subcategory=%q, slug=%q)`",
				memParts[0], memParts[1], memParts[2]))
		}
	}
	return strings.Join(parts, ". ")
}

func formatIndexHint(rawJSON string) string {
	// Parse the JSON array of IndexEntry objects into human-readable format
	var entries []struct {
		Category    string  `json:"category"`
		Subcategory *string `json:"subcategory,omitempty"`
		DocCount    int     `json:"doc_count"`
		Topics      string  `json:"topics"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &entries); err != nil {
		return "## Knowledge Base Index\n" + rawJSON
	}

	var b strings.Builder
	b.WriteString("## Knowledge Base Index\n")
	for _, e := range entries {
		path := e.Category
		if e.Subcategory != nil {
			path += "/" + *e.Subcategory
		}
		b.WriteString(fmt.Sprintf("%s (%d docs) — %s\n", path, e.DocCount, e.Topics))
	}
	return b.String()
}
```

- [ ] **Step 3: Update imports**

Add to imports: `"context"`, `"net/http"`, `"time"`, `"bytes"`.
Remove `"io"` only if no longer used (it is — keep it for `io.LimitReader`).

- [ ] **Step 4: Verify compilation**

Run: `cd ~/mystuff/goprojects/claude-hook-engine && go build ./...`
Expected: PASS

- [ ] **Step 5: Format**

Run: `cd ~/mystuff/goprojects/claude-hook-engine && gofmt -w .`

---

### Task 3: Add SessionStop Handler — Spawn Memory Agent

**Files:**
- Create: `~/mystuff/goprojects/claude-hook-engine/internal/hook/stop.go`

- [ ] **Step 1: Implement HandleSessionStop**

```go
// internal/hook/stop.go
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eliminyro/claude-hook-engine/internal/config"
)

type stopInput struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
}

// HandleSessionStop spawns memory-agent as a detached background process
// to extract and store session learnings.
func HandleSessionStop(r io.Reader, rulesPath string) (string, error) {
	// Recursion guard
	if os.Getenv("MEMORY_AGENT_INVOKED") == "1" {
		return "", nil
	}

	var inp stopInput
	if err := json.NewDecoder(r).Decode(&inp); err != nil {
		return "", fmt.Errorf("decoding session-stop input: %w", err)
	}

	cfg, err := config.Load(rulesPath)
	if err != nil {
		return "", fmt.Errorf("session-stop: %w", err)
	}

	binaryPath := cfg.MemoryAgent.BinaryPath
	if binaryPath == "" {
		return "", nil // memory-agent not configured
	}

	// Resolve transcript path
	transcriptPath := inp.TranscriptPath
	if transcriptPath == "" {
		// Fallback: construct from session ID
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		// Claude Code stores transcripts at ~/.claude/projects/<project>/<session_id>.jsonl
		// We need the CWD-based project path
		projectDir := projectDirFromCWD(home, inp.CWD)
		transcriptPath = filepath.Join(projectDir, inp.SessionID+".jsonl")
	}

	if _, err := os.Stat(transcriptPath); err != nil {
		slog.Debug("transcript not found, skipping capture", "path", transcriptPath)
		return "", nil
	}

	// Build command args
	args := []string{"capture"}
	if cfg.MemoryAgent.ConfigPath != "" {
		configPath := cfg.MemoryAgent.ConfigPath
		if strings.HasPrefix(configPath, "~") {
			home, _ := os.UserHomeDir()
			configPath = home + configPath[1:]
		}
		args = append(args, "-config", configPath)
	}
	args = append(args, transcriptPath)

	// Spawn detached — don't block the session
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "MEMORY_AGENT_INVOKED=1")
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		slog.Debug("failed to spawn memory-agent", "error", err)
		return "", nil // non-fatal
	}

	// Detach — don't wait for it
	go cmd.Wait()

	slog.Debug("spawned memory-agent", "pid", cmd.Process.Pid, "transcript", transcriptPath)
	return "", nil
}

// projectDirFromCWD converts a CWD path to the Claude Code project directory path.
// Claude Code uses: ~/.claude/projects/-<sanitized-cwd>/
func projectDirFromCWD(home, cwd string) string {
	sanitized := strings.ReplaceAll(cwd, "/", "-")
	return filepath.Join(home, ".claude", "projects", sanitized)
}
```

- [ ] **Step 2: Add dispatch in cmd/main.go**

Add to the switch statement in `main()`:

```go
case "session-stop":
	if err := runSessionStop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
```

Add the `runSessionStop` function:

```go
func runSessionStop() error {
	output, err := hook.HandleSessionStop(os.Stdin, rulesPath())
	if err != nil {
		return err
	}
	if output != "" {
		fmt.Print(output)
	}
	return nil
}
```

Update usage string to include `session-stop`.

- [ ] **Step 3: Verify compilation**

Run: `cd ~/mystuff/goprojects/claude-hook-engine && go build ./...`
Expected: PASS

- [ ] **Step 4: Format**

Run: `cd ~/mystuff/goprojects/claude-hook-engine && gofmt -w .`

---

### Task 4: Register Stop Hook in settings.json

**Files:**
- Modify: `~/.claude/settings.json`

- [ ] **Step 1: Add Stop hook registration**

Add to the `hooks` section in `~/.claude/settings.json`:

```json
"Stop": [
  {
    "command": "claude-hook-engine session-stop"
  }
]
```

This fires when a Claude Code session ends. The hook engine reads the session info from stdin and spawns memory-agent in the background.

NOTE: Do not add this yet — only add once memory-agent is installed on PATH and configured. Document this as a manual step.

- [ ] **Step 2: Create reference doc**

Create `~/.claude/skills/ingest/README.md` with setup instructions for the user:

```markdown
# Knowledge Compilation Setup

## Prerequisites
1. memory-agent binary on PATH: `go install github.com/eliminyro/memory-agent/cmd/memory-agent@latest`
2. memory-capture.yaml at ~/.claude/memory-capture.yaml
3. memory-mcp API key at ~/.config/memory-agent/credentials (chmod 600)

## Hook Registration
Add to ~/.claude/settings.json hooks:
- Stop: `claude-hook-engine session-stop` (auto-capture)
- SessionStart already handles index injection (rebuild hook engine)

## Verify
- Run `memory-agent capture <session.jsonl>` manually on a recent session
- Check ~/.claude/logs/memory-capture.log for results
```

---

### Task 5: /ingest Skill

**Files:**
- Create: `~/.claude/skills/ingest/ingest.md`

- [ ] **Step 1: Write the skill file**

```markdown
---
name: ingest
description: Ingest external sources into the knowledge base. Reads articles, docs, papers — extracts key concepts and stores them in memory-mcp with user approval.
---

# /ingest — External Source Ingestion

Ingest an external source (URL, file, or PDF) into the knowledge base.

## Usage
```
/ingest https://some-article.com
/ingest ~/Downloads/paper.pdf
/ingest ~/notes/rough-thoughts.md
```

## Workflow

1. **Read the source:**
   - URL: use `ctx_fetch_and_index` or `WebFetch` to fetch the content
   - File/PDF: use the `Read` tool
   - Parse and understand the content

2. **Extract key concepts:**
   For each concept, determine:
   - `category/subcategory/slug` — where it belongs (learnings/go/topic, learnings/infrastructure/topic, etc.)
   - A descriptive heading
   - The content written as durable facts (not "the article says X" but "X works by doing Y")
   - Whether it's new or should update an existing doc

3. **Check for overlap:**
   For each candidate, call `mcp__memory__search_memory(query=<candidate content>)` to find similar existing knowledge.

4. **Present to user for approval:**
   Show a summary table:
   ```
   Extracted N concepts:
   1. learnings/go/context-propagation — NEW — "Context Propagation Patterns"
   2. learnings/infrastructure/terraform — MERGE into existing (85% similar) — "State Locking"
   3. learnings/go/error-handling — SKIP (near-duplicate)
   ```
   Ask: "Accept all, or adjust? (You can reject individual items by number)"

5. **Store approved candidates:**
   - NEW items: `mcp__memory__store_memory(category, subcategory, slug, content)`
   - MERGE items: `mcp__memory__update_section(section_id, content)`
   - Report what was stored

## Rules
- Always check for duplicates before storing
- Write content as standalone facts, not article references
- Let the user adjust categories and slugs before storing
- If the source is very long, focus on the 3-7 most important concepts
- Use the existing category taxonomy: learnings (go, infrastructure, cicd, observability, tools, homelab), preferences, projects
```

- [ ] **Step 2: Verify skill shows up**

Run Claude Code, type `/ingest` — it should appear in the skill list.

---

### Task 6: Scheduled Weekly Lint (Documentation Only)

This is a `/schedule` trigger — not code to write, just a config to document. The user sets this up manually.

- [ ] **Step 1: Document the scheduled lint command**

Add to the README or as a note: to set up weekly lint, run:

```
/schedule create --name "weekly-kb-lint" --cron "0 9 * * 1" --prompt "Run mcp__memory__lint_memory() and review findings. For near-duplicates, merge the less complete doc into the more complete one. For stale docs, check if the content is still accurate. Report what you fixed."
```

This runs every Monday at 9 AM. The scheduled agent uses Haiku by default and has access to MCP tools.

---

## Self-Review

**Spec coverage:**
- Section 3 (SessionStart index injection): Task 2
- Section 3 (CwdChanged): Same hook fires on CwdChanged — already registered in settings.json
- Section 3 (Stop → spawn capture): Tasks 3, 4
- Section 3 (Recursion guard): Task 3 (MEMORY_AGENT_INVOKED env var)
- Section 4 (/ingest skill): Task 5
- Section 5 (Weekly lint): Task 6
- Section 5 (Index regeneration — not scheduled): Covered by Task 2 (live on every session start)
- Section 5 (Full re-compilation — future): Correctly omitted

**Placeholder scan:** No TBDs. All code is complete.

**Type consistency:** `stopInput` matches what Claude Code sends. `MemoryMCPConfig` and `MemoryAgentConfig` used consistently between config and session handlers.
