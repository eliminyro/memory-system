> **Builds on `doc-type-policies`.** Groups 1 and 2 assume the `doc_type_policies` table exists and
> that every curation mechanism reads it. Both ship inside the re-cut v1.0.0, so this is an
> implementation ordering note, not a release dependency.

## 1. Model and schema

- [ ] 1.1 Add `DocTypePrompt = "prompt"` to the doc_type consts and to `ValidDocTypes` in `internal/models/document.go`
- [ ] 1.2 Add a `case "prompts"` to `InferDocType` in `internal/models/staleness.go` returning `DocTypePrompt` (classification stays in Go — see `doc-type-policies` D4)
- [ ] 1.3 Add `PromptScope *string` (`gorm:"size:500"`) to `models.Document` with a migration adding `documents.prompt_scope TEXT NULL`
- [ ] 1.4 Test: `InferDocType("prompts", ...)` returns `DocTypePrompt`

## 2. Curation policy

- [ ] 2.1 Add the `prompt` seed row: `staleness_days 0`, `duplicate_guard`/`cleanup_scan`/`lint_stale_check`/`prunable`/`embed`/`default_search` all false, `write_mode replace`, `subcategory required`, empty `rules`
- [ ] 2.2 Tests asserting the row's effect end to end — stale prompt served in full under `staleness_mode="hard"`; near-duplicate prompt write not blocked; scanner enqueues no prompt pair; lint reports no prompt as stale; unfiltered search omits prompts while a filtered search returns them
- [ ] 2.3 Test that editing the row changes the behavior, since that is the point of it being configuration (flip `embed` and `default_search` true, re-store a prompt, assert it is embedded and appears in unfiltered search)

## 3. Tenant scoping

- [ ] 3.1 Restrict prompt reads to the home tenant: prompt queries never pass `readableTenants` output, and the shared read paths (`get_document`, `list_documents`, `search_memory`, `get_related`) drop non-home tenants when the category is `prompts` or doc_type is `prompt`
- [ ] 3.2 Tests: a common-pool prompt document is invisible; a viewer-granted tenant's prompt document is invisible on every read path while its non-prompt documents stay visible

## 4. Search exclusion

- [ ] 4.1 Verify the `embed` and `default_search` rules from `doc-type-policies` cover prompts with no prompt-specific code — no embeddings written, absent from both arms of an unfiltered query; if not, that is a gap in the dependency, fix it there
- [ ] 4.2 Note the exclusion and the opt-in filter in the `search_memory` tool description in `internal/mcp/tools.go`

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
