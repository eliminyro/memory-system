## Context

Agent instruction files are synced by hand across machines today. memory-system already holds the
knowledge those instructions sit beside, so the instructions can live in the same tenant and be
fetched at session start.

Current state that constrains the design (verified against source on this branch):

- `InferDocType` (`internal/models/staleness.go:20`) is a category switch. Doc_type drives the
  staleness threshold, which drives whether hard mode withholds section content.
- The duplicate guard already has an exemption hook: `!models.IsEpisodic(docType)`
  (`internal/service/memory.go:1123`).
- `internal/repository/lint.go` filters episodic types out of stale checks with
  `doc_type <> ALL(?)` bound from `models.EpisodicDocTypes()`.
- The cleanup scanner (`internal/cleanup/scanner.go:118`) only enqueues near-duplicate pairs and
  prunes `mutation_history`. It does not archive documents. `ArchiveByID` has exactly one caller:
  `link_documents ... supersedes`. `Pinned` / `LastAccessedAt` exist as fields with no sweep reading
  them — access-recency eviction is modeled but not wired.
- `readableTenants` (`internal/service/memory.go:597`) always adds the home tenant and
  `models.BootstrapTenantID` (common pool), then every tenant granted viewer. Every read path uses it.
- `Document.ContentHash` is `hex(sha256(raw markdown))`, already maintained for the write guard's
  exact-duplicate short-circuit.
- `go-sdk` v1.7.0 is a dependency. Nothing calls `AddPrompt`; only `AddTool`.

The client is `claude-hook-engine`. `HandleSessionStart` (`internal/hook/session.go:26`) already
POSTs a `tools/call` to memory-system and returns `additionalContext`, so the transport and auth are
in place there.

## Goals / Non-Goals

**Goals:**

- One store for agent instructions, reachable from any machine.
- Instruction text is never withheld, deduplicated, merged, or evicted by curation machinery.
- A retrieval call shaped for assembly: complete, ordered, cheap to diff.
- Prompt documents never resolve from another tenant.

**Non-Goals:**

- The client side. This change specs what the client can rely on; `claude-hook-engine` implements the
  render-to-disk and injection lanes in its own repo.
- MCP `prompts/*` capability via `AddPrompt`. Separate change, builds on this storage.
- Migrating the existing local instruction files. That is a one-time import the user runs.
- Templating or variable interpolation inside prompt text. Documents are served verbatim.

## Decisions

**D1 — A third exemption class, not a wider episodic set.**

`episodicDocTypes` currently means "append-per-day, exempt from curation, `journal` prunable and
`handoff` permanent". Prompts share the exemption but not the shape: they are edited in place, and
adding `prompt` to the episodic map would also have to be added to `neverPruneDocTypes`, leaving
"episodic" meaning nothing beyond "exempt". Introduce `IsInstruction(docType) bool` (or
`exemptDocTypes` covering both classes) and switch the four call sites to the broader predicate:
duplicate guard, lint stale filter, cleanup scan filter, staleness check. `EpisodicDocTypes()` keeps
its current meaning for the SQL arrays that mean "journal or handoff".

*Alternative considered:* add `prompt` to `episodicDocTypes` plus `neverPruneDocTypes`. Two lines
instead of a new predicate, but it makes `IsEpisodic` a lie and the next reader has to discover that
"episodic" now includes something edited in place.

**D2 — Scope is one nullable text column on `documents`, not a binding table.**

`prompt_scope TEXT NULL`: empty means always-apply, non-empty is a space-separated pattern list
matched against the client's scope. One column, one meaning, no join.

*Alternative considered:* a `prompt_bindings(document_id, agent, project, mode, ordinal)` table.
Correct relational modeling and it would support one document targeting several agents, which the
path convention cannot. Rejected for now: a document is authored for one agent, and the table buys
flexibility nothing needs yet. If cross-agent sharing becomes real, the table is the migration.

*Alternative considered:* encode scope in the slug (`hilo-testing`). No schema change, but it
overloads the slug and makes the scope unqueryable.

**D3 — No explicit assembly-order field.**

Sort by `(subcategory, slug)` and let the client impose precedence. In the render-to-disk lane the
client's own file naming and its `CLAUDE.md` import list already decide order, so a server-side
ordinal would be a second source of truth for the same thing.

*Alternative considered:* an `ordinal` column, or numeric slug prefixes (`10-persona`). Revisit only
if a client appears that cannot control its own assembly.

**D4 — Own-tenant-only enforced at the query layer, not by filtering results.**

Prompt reads pass the home tenant alone rather than calling `readableTenants`. Filtering after the
fact would leave the cross-tenant path one forgotten call site away from leaking. Concretely: the
retrieval query is tenant-scoped by construction, and the shared read paths
(`get_document`, `list_documents`, `search_memory`, `get_related`) drop non-home tenants when the
category is `prompts` or the doc_type is `prompt`.

**D5 — Search exclusion is a default predicate, not a post-filter.**

Add `AND d.doc_type <> 'prompt'` to the search candidate query unless the caller named the category
or doc_type explicitly. Excluding post-fusion would waste candidate slots on documents that are then
dropped, shrinking the effective result set.

**D6 — Reuse `ContentHash` for change detection.**

It is already computed on write. The retrieval response carries it per document, and hash-only mode
returns the document list without section content so a client with an up-to-date cache does one cheap
call and no writes.

**D7 — Both MCP tool and REST endpoint.**

The MCP tool serves agents already holding a connection. The REST endpoint serves a client that runs
before or outside an agent session — a launch wrapper rendering files to disk should not have to
speak JSON-RPC over SSE and parse `data:` lines, which is what the hook engine does today.

## Risks / Trade-offs

- **A prompt document is executable instruction text, so the memory tenant becomes an
  instruction-injection surface.** → Own-tenant-only resolution (D4), and no cross-tenant grant can
  reach a prompt document. Tenant API keys already gate writes.
- **Server unreachable at session start means no instructions.** → The render-to-disk lane leaves the
  last-known-good file on disk, so a failed fetch degrades to yesterday's rules rather than none.
  This is a client-side property, but it is the reason the always-apply lane renders files instead of
  injecting context.
- **`additionalContext` carries less authority than a `CLAUDE.md` @-import.** → Only situational
  prompts go through that lane. Behavioral rules that must override defaults render to disk and stay
  @-imported.
- **Exempting prompts from the duplicate guard means real accidental duplicates land silently.** →
  Accepted. The set is small and hand-curated; a wrong merge of two agents' persona docs costs more
  than a stray duplicate.
- **`prompt_scope` on `documents` is a column that is meaningless for every other doc_type.** →
  Accepted for one column. A second prompt-only column is the signal to move to D2's binding table.
- **Search exclusion hides prompt documents from the tool an agent reaches for first.** → The MCP
  tool description states the exclusion and names the filter that opts back in.

## Migration Plan

1. Migration adds `documents.prompt_scope TEXT NULL` and seeds the `prompt` row in
   `staleness_thresholds`. Both are additive; no backfill, no existing row touched.
2. Deploy server. Existing callers are unaffected — `prompts` is a category nothing currently writes.
3. Import the local instruction files into `prompts/<agent>/<slug>` (one-time, via the existing import
   path).
4. Client work lands separately in `claude-hook-engine`. Until it does, the files on disk stay the
   source of truth and nothing regresses.

Rollback: revert the server deploy. The added column and threshold row are inert to older code, so
they can stay.

## Open Questions

- Does `<agent>` as subcategory hold up when the same instruction set is wanted by two agents, or does
  that arrive soon enough to justify D2's binding table now?
- Should the scope pattern language be path globs, project names, or both? The spec says both are
  matched against one client-supplied string; the matcher's precedence rules need pinning down before
  the client relies on them.
- Should `lint_memory` gain a prompt-specific check (an agent with no always-apply documents, a scope
  pattern matching nothing) to replace the staleness signal it loses?
