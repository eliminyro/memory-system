## 1. Model and schema

- [ ] 1.1 Add `DocTypePrompt = "prompt"` to the doc_type consts and to `ValidDocTypes` in `internal/models/document.go`
- [ ] 1.2 Add a `case "prompts"` to `InferDocType` in `internal/models/staleness.go` returning `DocTypePrompt`
- [ ] 1.3 Add the `IsInstruction` / broader exemption predicate per design D1, keeping `IsEpisodic` and `EpisodicDocTypes()` meaning exactly what they mean today; add a `DocTypePrompt` case to the `TestIsEpisodic` table so the two classes stay distinct
- [ ] 1.4 Add `PromptScope *string` (`gorm:"size:500"`) to `models.Document` with a migration adding `documents.prompt_scope TEXT NULL`
- [ ] 1.5 Seed the `prompt` row in `staleness_thresholds` in `internal/database/database.go` (value is inert — prompts are never staleness-checked — so pick a large number and say why in one line)
- [ ] 1.6 Unit tests: `InferDocType("prompts", ...)`, the exemption predicate over every member of `ValidDocTypes`

## 2. Curation exemptions

- [ ] 2.1 Bypass the write-time duplicate guard for prompt doc_type at `internal/service/memory.go:1123`
- [ ] 2.2 Exclude prompt documents from the lint stale-document check in `internal/repository/lint.go` (extend the `doc_type <> ALL(?)` binding to the exemption set)
- [ ] 2.3 Exclude prompt documents from `FindNearDuplicatePairs` so the cleanup scanner enqueues no prompt pairs
- [ ] 2.4 Skip staleness evaluation for prompt sections in `internal/staleness/staleness.go` so no prompt section is ever marked `needs_verification` or withheld under hard mode
- [ ] 2.5 Treat prompt documents as never-evictable in whatever reads `Pinned` (nothing does today — assert it in a test so a future eviction sweep cannot silently pick them up)
- [ ] 2.6 Tests: stale prompt served in full under `staleness_mode="hard"`; duplicate-guard bypass; scanner skips prompt pairs; lint omits prompts

## 3. Tenant scoping

- [ ] 3.1 Restrict prompt reads to the home tenant: prompt queries never pass `readableTenants` output, and the shared read paths (`get_document`, `list_documents`, `search_memory`, `get_related`) drop non-home tenants when the category is `prompts` or doc_type is `prompt`
- [ ] 3.2 Tests: a common-pool prompt document is invisible; a viewer-granted tenant's prompt document is invisible on every read path while its non-prompt documents stay visible

## 4. Search exclusion

- [ ] 4.1 Add the default `doc_type <> 'prompt'` predicate to the search candidate query in `internal/repository/section.go`, suppressed when the caller named `category="prompts"` or `doc_type="prompt"`
- [ ] 4.2 Note the exclusion and the opt-in filter in the `search_memory` tool description in `internal/mcp/tools.go`
- [ ] 4.3 Tests: unfiltered search omits a prompt document that lexically matches; explicit category or doc_type filter returns it

## 5. Retrieval

- [ ] 5.1 Repository query: prompt documents for one agent (subcategory) with sections, ordered by `(subcategory, slug, section.ordinal)`, home tenant only
- [ ] 5.2 Scope matcher: empty `prompt_scope` = always-apply lane; non-empty = space-separated patterns matched against the caller's scope string, placing hits in the scoped lane. Pin the precedence rules (see design Open Questions) before wiring the client
- [ ] 5.3 Service method returning the assembled result: per document path, title, scope, lane, `content_hash`, ordered sections; plus a hash-only mode that omits section content
- [ ] 5.4 Register the `get_prompts` MCP tool in `internal/mcp/tools.go` (agent required, scope optional, hash-only flag)
- [ ] 5.5 Add the REST endpoint under `/api/` in `internal/server/api.go` sharing the same service method
- [ ] 5.6 Tests: byte-identical repeat calls; documented sort order; lane split for scoped/unscoped/non-matching; hash changes after a section edit; hash-only omits content; MCP and REST return the same set; REST honors own-tenant-only

## 6. Write path

- [ ] 6.1 Accept and update `prompt_scope` through `store_memory` (and the REST equivalent), returning it on reads
- [ ] 6.2 Reject a non-empty `prompt_scope` on a non-prompt document rather than storing a value nothing will read
- [ ] 6.3 Tests: set, update, clear, and the rejection

## 7. Docs and close-out

- [ ] 7.1 Document the `prompts/<agent>/<slug>` convention, the scope field, the two delivery lanes, and the curation exemptions in the README/docs routing section
- [ ] 7.2 State the client contract `claude-hook-engine` will build against: which call, which lane renders to disk, which lane injects, how hashes gate rewrites
- [ ] 7.3 `gofmt -w .` and `golangci-lint run` clean; full test suite green
- [ ] 7.4 One-time import of the local instruction files into `prompts/<agent>/<slug>` (manual, after deploy — not part of the commit)
