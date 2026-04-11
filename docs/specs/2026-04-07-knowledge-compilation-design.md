# Knowledge Compilation System — Design Spec

## Overview

Extend memory-mcp with knowledge compilation capabilities inspired by Karpathy's LLM Wiki pattern and Cole's claude-memory-compiler. The system automatically captures session learnings, compiles external sources into structured knowledge, and maintains KB health — all without adding LLM dependencies to the server.

**Architecture: Edge Intelligence (Hybrid C)**
- Server (memory-mcp): structural bookkeeping — index generation, relationship discovery, lint. No LLM.
- Client (new Go binary + hooks + skills): LLM-powered adversarial capture pipeline, interactive ingestion.
- Hook engine: trigger and injection layer. Stays focused on its existing role.

## Components

### 1. Server-Side — New MCP Tools (memory-mcp)

Four new tools added to `internal/mcp/tools.go`.

#### `generate_index`

Produces a tiered catalog of the tenant's knowledge base.

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `depth` | enum | `summary` | `summary` / `category` / `full` |
| `category` | string? | nil | Filter to specific category |

**Depth levels:**

- `summary`: One line per subcategory — doc count + topic keywords from titles. Stays under 2K tokens at 1000+ docs. SQL: `GROUP BY category, subcategory` with `string_agg(title, ', ')`.
- `category`: All docs in one category, one-line title + slug per doc.
- `full`: All docs with titles and slugs. For small tenants only.

Response format (summary):
```
learnings/go              (14 docs) — GORM, chi, workers, concurrency, testing
learnings/infrastructure  (8 docs)  — terraform, k8s, vault, networking
projects/hilo             (3 docs)  — state, api-design, deployment
preferences               (2 docs)  — coding-style, communication
```

Includes bootstrap tenant docs. Pure SQL aggregation — no LLM, no embedding calls.

#### `get_related`

Finds documents semantically related to a given document.

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `document_id` | uuid | required | Target document |
| `limit` | int | 5 | Max results |

Implementation: Average the target doc's section embeddings, then `ORDER BY embedding <=> avg_vector` across all tenant + bootstrap sections. Groups by document, returns doc paths + similarity scores.

No embedding API calls — uses vectors already stored by `store_memory`.

#### `lint_memory`

Health report for the tenant's knowledge base.

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `checks` | string[]? | all | Filter to specific checks |
| `thresholds` | object? | defaults | Override threshold values |

**Checks (all SQL, no LLM):**

| Check | Detects | Method |
|-------|---------|--------|
| `stale` | Docs not updated in N days | `updated_at` threshold |
| `sparse` | Docs with 1 short section | Section count + content length |
| `near_duplicate` | Suspiciously similar docs | Pairwise cosine similarity above threshold |
| `orphan` | Isolated sections with no related content | Max cosine similarity to any other section below threshold |
| `empty_category` | Subcategories with only 1 doc | Aggregation query |
| `broken_reference` | Cross-links to deleted docs | FK check (requires references field) |

Returns JSON array: `{check, severity, document_path, message}`.

#### `references` field (optional, future)

Add an optional `references` column on sections for explicit cross-links between documents. Enables the `broken_reference` lint check and richer `get_related` results. Not required for v1 — cosine similarity handles implicit relationships.

### 2. Client-Side — Adversarial Capture Pipeline

A new Go binary (`memory-agent`) that runs the LLM-powered knowledge extraction pipeline.

#### Binary: `memory-agent`

Separate from the hook engine. Single responsibility: capture session learnings via an adversarial two-agent pipeline.

**Why separate from hook engine:**
- Hook engine's job is tool call interception and rule application
- Knowledge capture is a different concern with different dependencies (Claude API/SDK, memory-mcp HTTP client)
- Keeps both binaries focused and testable

**Config file:** `~/.claude/memory-capture.yaml`

```yaml
auth: sdk                          # "sdk" (subscription) or "api" (API key)
api_key: ""                        # only when auth=api
model: claude-haiku-4-5-20251001   # default model for both agents
extractor_model: ""                # override for extractor, falls back to model
reviewer_model: ""                 # override for reviewer, falls back to model
memory_mcp_url: https://memory-mcp.a11s.dev/mcp
memory_mcp_api_key: file://~/.config/memory-agent/credentials  # secret source for MCP auth
```

- `auth: sdk` — uses `~/.claude/.credentials.json` (subscription)
- `auth: api` — uses provided API key
- `memory_mcp_api_key` — supports multiple secret sources:
  - `file://path` — local file, `chmod 600` (default, simplest onboarding)
  - `gcp://project/secret-name` — GCP Secret Manager via ADC
  - `env://VAR_NAME` — environment variable (CI/containers)
  - `literal://value` — plain value (testing only)
- Model configurable per-agent. Supports swapping providers later by adding backends.
- No external secret manager dependency required — file-based is the default.

**Invocation:**

```bash
memory-agent capture <session_jsonl_path>
```

#### Agent 1: Extractor

- Model: Haiku (configurable)
- Input: Session transcript (JSONL)
- System prompt: knows the category taxonomy, what durable knowledge looks like vs. ephemeral chatter
- Output: JSON array of candidates:
  ```json
  [
    {
      "path": "learnings/go/context-propagation",
      "heading": "Context Propagation Patterns",
      "content": "In Go, context.Context should be...",
      "type": "new"
    },
    {
      "path": "learnings/infrastructure/terraform-state",
      "heading": "Remote State Locking",
      "content": "When using S3 backend...",
      "type": "update"
    }
  ]
  ```
- Writes candidates as durable facts ("X works by doing Y"), not session references ("we discussed X")
- Extracts 0-7 candidates per session. Zero is valid — not every session produces learnings.

#### Agent 2: Reviewer

- Model: Haiku (configurable)
- Input: Each candidate from Agent 1 + existing KB context
- For each candidate:
  1. Calls `search_memory(query=candidate.content)` via memory-mcp HTTP
  2. If near-duplicate found (high similarity): **reject** with reason
  3. If existing doc at same path: recommend `update_section` instead of `store_memory`
  4. Challenges: Is this durable? Would it be useful in 3 months? Is it too specific to this session?
  5. Verdict: `accept`, `merge` (into existing doc), or `reject`
- Output: filtered list with actions

#### Writer Step (no agent, just code)

- `accept` → `store_memory` via memory-mcp HTTP
- `merge` → `update_section` on identified target
- `reject` → logged to `~/.claude/logs/memory-capture.log`

**State files:**
- `~/.claude/state/memory-capture/last-capture.json` — session ID + timestamp, prevents double-processing
- `~/.claude/logs/memory-capture.log` — all decisions (accepts, merges, rejects with reasons)

**Cost:** ~$0.01-0.03 per session with Haiku. Runs in background, results available by next session.

### 3. Hook Engine Integration

Minimal changes to the existing hook engine.

#### SessionStart — Index Injection

Extend `claude-hook-engine session-start` to:

1. Call memory-mcp `generate_index(depth="summary")` via HTTP POST
2. Call memory-mcp `lint_memory` (lightweight SQL) for health summary
3. Inject both into session context:

```
## Knowledge Base Index
learnings/go              (14 docs) — GORM, chi, workers, concurrency, testing
learnings/infrastructure  (8 docs)  — terraform, k8s, vault, networking
projects/hilo             (3 docs)  — state, api-design, deployment

Last capture: 2026-04-07 (3 new learnings from previous session)
KB health: 2 stale docs, 1 near-duplicate pair
```

- Auth: API key resolved via same `gcp://`, `env://`, `literal://` scheme as memory-agent config
- Fast: SQL aggregation, no LLM, <100ms
- Falls back gracefully if memory-mcp is unreachable (inject nothing, session continues normally)

#### CwdChanged — Project-Scoped Index

Same hook already fires on `CwdChanged`. Could filter index by project mapping (e.g., in hilo repo, prioritize `projects/hilo` docs). Optional enhancement, not v1.

#### Stop — Spawn Capture Pipeline

Extend Stop hook to:

1. Spawn `memory-agent capture <session_jsonl_path>` as a detached background process
2. Return immediately — no session blocking

~10-20 lines of new code in the hook engine. The hook engine stays a trigger; `memory-agent` owns the pipeline.

**Recursion guard:** Set `MEMORY_AGENT_INVOKED=1` env var when spawning. The Stop hook checks this and skips if set — prevents infinite loops if the agent SDK triggers Claude Code sessions.

### 4. `/ingest` Skill — External Source Compilation

Claude Code skill for interactive knowledge ingestion from external sources.

**Location:** `~/.claude/skills/ingest/`

**Invocation:**
```
/ingest https://some-article.com
/ingest ~/Downloads/paper.pdf
/ingest ~/notes/rough-thoughts-on-caching.md
```

**Flow (runs in current session, interactive):**

1. **Read source** — fetch URL, read file, or read PDF
2. **Extract** — current session model reads source, extracts key concepts as candidates with `category/subcategory/slug`, heading, content
3. **Review against KB** — for each candidate, `search_memory` checks for overlap. Presents summary:
   ```
   3 concepts extracted:
   - learnings/go/context-propagation — NEW
   - learnings/infrastructure/terraform-state — MERGE into existing (82% similarity)
   - learnings/go/error-handling — SKIP (near-duplicate)
   ```
4. **User confirms** — approve, reject, or adjust before writes
5. **Store** — accepted candidates get `store_memory`'d or `update_section`'d

**Key difference from auto-capture:** `/ingest` is interactive with user approval. Auto-capture (Stop hook) is fully autonomous. External sources are higher-stakes — user may want to recategorize or reword.

**Implementation:** Markdown skill file that orchestrates existing MCP tools (`search_memory`, `store_memory`, `update_section`). No separate binary needed.

### 5. Scheduled Maintenance — Lint + Compilation Cycles

#### Weekly Lint Agent

Via `/schedule` skill / remote triggers:

1. Runs `lint_memory` → gets health report
2. **Near-duplicates**: calls `get_related` on both docs, uses Haiku to merge into one, deletes the other
3. **Stale docs**: flags but doesn't auto-delete (logs to maintenance record)
4. **Sparse docs**: searches for related content that could be merged in
5. Posts summary to `projects/memory-mcp/maintenance-log` doc in the KB

Model: Haiku. Auth: configurable (same as memory-agent).

#### Index Regeneration

Not scheduled — `generate_index` called live by SessionStart every time. Fast SQL query.

If performance degrades at 1000+ docs, add a materialized view refreshed on writes. Not v1.

#### Full Re-compilation (future, not v1)

Monthly scheduled agent that reads entire KB via `list_documents` + `get_document`, does a quality pass — tightens connections, rewrites weak articles, updates summaries. The adversarial capture + weekly lint should keep quality high enough without this initially.

## Component Summary

| Component | Location | Trigger | LLM? | Language |
|-----------|----------|---------|------|----------|
| `generate_index` | memory-mcp server | SessionStart hook / on-demand | No | Go |
| `get_related` | memory-mcp server | On-demand | No | Go |
| `lint_memory` | memory-mcp server | Weekly schedule + on-demand | No | Go |
| Index injection | Hook engine (`session-start`) | SessionStart | No | Go |
| Capture spawn | Hook engine (`Stop`) | Stop | No | Go |
| Capture pipeline | `memory-agent` binary | Spawned by hook | Haiku | Go |
| `/ingest` skill | Claude Code skill | Manual | Session model | Markdown |
| Weekly lint agent | Scheduled remote trigger | Cron | Haiku | Go/scheduled |

## Scaling

- **<500 docs**: `summary` index fits comfortably in context. Lint runs fast.
- **500-2000 docs**: `summary` stays compact (aggregated per subcategory). Lint near-duplicate check may slow — add pairwise similarity caching or limit to recent docs.
- **2000+ docs**: Consider materialized view for index. Add category-scoped lint runs. `get_related` stays fast (HNSW index handles it).

## Dependencies

- **memory-mcp**: PostgreSQL, pgvector, GCP Vertex AI (existing)
- **memory-agent**: Go, Anthropic API (Claude SDK or API key), memory-mcp HTTP client
- **Hook engine**: Existing binary, minimal additions (~20 lines)
- **`/ingest` skill**: Pure Claude Code skill, no new dependencies
- **Scheduled maintenance**: Existing `/schedule` infrastructure
