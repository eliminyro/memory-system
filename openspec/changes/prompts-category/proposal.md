## Why

Agent operating instructions (persona, style rules, workflow rules, per-project hints) currently live
as local markdown files that every machine has to sync by hand. The knowledge they sit next to is
already centralized in memory-system, so the instructions can be too — one store, reachable from any
machine, no file copying. That also removes the drift that shows up whenever the same rule exists in
two places.

Storing them in an existing category does not work: every curation mechanism in the system (staleness
withholding, duplicate guard, cleanup scan, semantic search) is built for knowledge that rots and
should be deduplicated. Instructions do neither, and hard-mode staleness withholding an agent's
persona doc means the agent silently loses its rules mid-session.

## What Changes

- New `prompts` top-level category with its own `prompt` doc_type.
- Prompt docs are exempt from all curation machinery: no staleness withholding, no lint stale-check,
  no near-duplicate cleanup scan, no write-time duplicate guard, never pruned. They are not episodic
  (edited in place, not appended per day), so this is a new exemption class rather than a widening of
  the existing episodic set.
- Prompt docs are excluded from `search_memory` results by default, with explicit opt-in through the
  existing `category` / `doc_type` filters.
- Prompt reads resolve **own-tenant only** — never the common pool, never a granted tenant. A prompt
  doc is instructions the agent will execute, so cross-tenant resolution would let another tenant's
  document become your system prompt.
- New retrieval path built for assembly rather than search: fetch every prompt doc matching a scope,
  in a deterministic order, with full section text. Exposed as an MCP tool and a REST endpoint.
- Prompt docs carry targeting metadata (which agent, which project/path) and a delivery mode
  (`always` vs `on-demand`) so a client can select the right subset for a session.

Out of scope, deliberately:

- The client side. `claude-hook-engine` consumes the retrieval API to render always-apply docs to disk
  and inject scoped ones as SessionStart `additionalContext`. This change specs the server contract
  it depends on, not its internals.
- MCP `prompts/*` capability (`AddPrompt`). Nothing in the server registers prompts today; surfacing
  on-demand docs as slash commands is a follow-up that builds on this storage.

## Capabilities

### New Capabilities

- `prompt-storage`: the `prompts` category and `prompt` doc_type — validation, doc_type inference,
  the curation exemption class, and own-tenant-only read resolution.
- `prompt-retrieval`: deterministic scope-based assembly of prompt docs over MCP and REST, including
  targeting metadata and delivery mode.

### Modified Capabilities

None. `openspec/specs/` is empty (OpenSpec was initialized for this change), so the existing
behaviors this touches — staleness, search filtering, tenant read scope — have no spec files to
delta against. Their current behavior is captured as constraints in `design.md`.

## Impact

Server code:

- `internal/models/document.go` — `DocTypePrompt` const, `ValidDocTypes` entry.
- `internal/models/staleness.go` — `InferDocType` case for `prompts`; new exemption predicate
  alongside `IsEpisodic` / `IsPrunableEpisodic`.
- `internal/database/database.go` — `staleness_thresholds` seed row.
- `internal/repository/section.go`, `internal/repository/lint.go`, `internal/cleanup/` — honor the
  new exemption in search filtering, lint stale-check, and the near-duplicate scan.
- `internal/service/memory.go` — duplicate-guard bypass, own-tenant-only read scope for prompts.
- `internal/mcp/tools.go`, `internal/server/api.go` — the retrieval tool and endpoint.

Clients: `claude-hook-engine` gains a second memory-system call at SessionStart. No breaking change
to existing tools — `prompts` is a new category and existing callers never name it.

Data: one new `staleness_thresholds` row. No migration of existing documents.
