## 1. Model and migration

- [ ] 1.1 `DocTypePolicy` model in `internal/models/` — `doc_type` PK, nullable typed columns (`staleness_days *int`, `duplicate_guard`/`cleanup_scan`/`lint_stale_check`/`embed`/`default_search`/`prunable` `*bool`, `write_mode`/`slug_format`/`subcategory` as `*string` with Go enum types), plus `Rules` JSONB. Nullable throughout so NULL-means-inherit stays distinguishable from `staleness_days: 0`
- [ ] 1.2 Replace `DefaultStalenessThresholds` with the seed rows from the spec: `reference` carries an explicit value for every column; the other seven set only what differs
- [ ] 1.3 Create `doc_type_policies` in `internal/database/database.go` with the typed columns and `rules`. No widening, backfill, or rename — v1.0.0 is re-cut, so there is no prior released schema (design Migration Plan)
- [ ] 1.4 Named CHECK constraints — `chk_staleness_days_range`, `chk_write_mode_enum`, `chk_slug_format_enum`, `chk_subcategory_enum`, `chk_default_search_needs_embed`, `chk_rules_is_object`. Thin on purpose; Go validates first (design D4)
- [ ] 1.5 Extend the seed loop to write full rows, keeping `ON CONFLICT DO NOTHING` so admin edits survive
- [ ] 1.6 Tests: every member of `ValidDocTypes` has a seeded row; a doc_type with no row falls back to `reference`; NULL inherits while `0` means never
- [ ] 1.7 Out-of-repo: a Kubernetes job for the maintainer's own instance — verify no threshold was hand-tuned away from the defaults, then drop the orphaned `staleness_thresholds` table once `doc_type_policies` has seeded

## 2. Policy store — boot load, admin invalidation, no timer

- [ ] 2.1 Replace `staleness.ThresholdStore`'s 5-minute TTL (`internal/staleness/staleness.go:68`) with a store loaded once at startup. Delete the TTL, `cachedAt`, and `refreshIfStale`
- [ ] 2.2 At boot: load all rows, resolve NULLs against `reference`, validate the merged set, hold the effective struct per doc_type in memory
- [ ] 2.3 Boot validation failure stops the server, naming the doc_type and the offending field
- [ ] 2.4 `Recompute()` on the store, called by the admin write path so an edit takes effect in process
- [ ] 2.5 Register the store with the `config-invalidation` listener, and add the `doc_type_policies` trigger to this change's migration, so other replicas converge
- [ ] 2.6 Tests: invalid row prevents startup with a message naming the doc_type; an admin write is visible to the next request; no query is issued against the policy table between writes

## 3. Validation

- [ ] 3.1 Validate a rule set before write: enum membership, `staleness_days >= 0`, `default_search: true` requires `embed: true`, `duplicate_guard: true` requires `write_mode: replace`
- [ ] 3.2 Validate the *merged* result, not just the submitted row — inheritance can produce a combination neither row shows alone, and CHECK cannot see across rows
- [ ] 3.3 Tests: each rejection returns a validation error naming the field, and no row changes

## 4. Admin surface

- [ ] 4.1 Read the policy table through the admin surface (`internal/mcp/admin_tools.go`, `internal/server/admin_api.go`), showing effective values and which were inherited
- [ ] 4.2 Write a policy row — instance admin only, not tenant manager; validate, persist, then `Recompute()`
- [ ] 4.3 Audit every write to `override_log`, matching `UpdateMyTenantSettings`
- [ ] 4.4 Tests: a tenant manager is refused; an admin write is audited; the write is visible immediately

## 5. Write path — new rules

- [ ] 5.1 Wire rule application into `StoreDocument` in the documented order: authz → classify → load rules → identity → content → resolve target → write mode → guards → embed → post-write links → history
- [ ] 5.2 `slug_format` validation (`any`/`date`/`datetime`/`kebab`) — rejects, never rewrites the slug, since `InferDocType` already consumed it
- [ ] 5.3 `subcategory` validation (`optional`/`required`/`forbidden`)
- [ ] 5.4 `write_mode` section handling at `internal/service/memory.go:1181-1192`: `replace` keeps today's delete-and-rewrite; `merge_sections` upserts by heading and leaves unlisted sections alone; `append_only` errors on a heading collision
- [ ] 5.5 Every violation is a validation error raised before any write, inside the existing transaction — no partial state
- [ ] 5.6 Tests: malformed journal slug rejected; missing handoff subcategory rejected; journal subcategory rejected; merge preserves an earlier section, replaces a matching heading, and is idempotent across two identical stores; `append_only` rejects a collision; a rejected write leaves no document, section, embedding, or history row

## 6. Replace the existing call sites

- [ ] 6.1 `internal/staleness/staleness.go` — `Check` skips evaluation when `staleness_days` is `0`
- [ ] 6.2 `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`
- [ ] 6.3 `internal/service/staleness_view.go` — both `IsEpisodic` calls read the rules
- [ ] 6.4 `internal/repository/lint.go` — the 3 `EpisodicDocTypes()` bindings become doc_type arrays computed in Go from the cached rules
- [ ] 6.5 `internal/repository/document.go:158` and `internal/service/memory.go:1255` — chaining reads `chain_previous` from `rules`
- [ ] 6.6 Assert nothing removes a document whose `prunable` is false (nothing implements retention today — the test guards a future sweep)
- [ ] 6.7 Delete `episodicDocTypes`, `neverPruneDocTypes`, `IsEpisodic`, `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes`. `InferDocType` and `ValidDocTypes` stay untouched

## 7. Embedding and default search

- [ ] 7.1 Skip embedding generation and storage on write when `embed` is false
- [ ] 7.2 `internal/repository/section.go` — add the `default_search` exclusion to BOTH arms of the candidate query (semantic and `s.tsv` keyword), suppressed when the caller named a category or doc_type
- [ ] 7.3 Confirm `get_document` and `list_documents` still return excluded documents unchanged
- [ ] 7.4 Tests: no embedding rows for `embed: false`; an `embed: true` + `default_search: false` doc_type is absent from both arms unfiltered but ranks semantically under an explicit filter; an `embed: false` doc_type does not leak through the keyword arm

## 8. Behavior preservation, and the three intended changes

- [ ] 8.1 Table test asserting each of the 8 doc_types resolves to the seed rule set from the spec
- [ ] 8.2 Retarget the existing episodic tests (`TestIsEpisodic` and the curation-exemption tests) at the rules, keeping their assertions
- [ ] 8.3 Everything unchanged: staleness, duplicate guard, lint, cleanup pairs, embeddings, and handoff chaining behave identically for all 8 doc_types
- [ ] 8.4 The three intended changes, each asserted separately: journals and handoffs absent from unfiltered search but semantically rankable when filtered; a store to an existing journal preserves earlier sections; a malformed journal slug rejected
- [ ] 8.5 `resume()` still walks handoff chains

## 9. Surface and docs

- [ ] 9.1 `lint_memory` finding for `rules` keys the server does not implement, so an experimental typo is visible rather than silent
- [ ] 9.2 `lint_memory` finding for a doc_type whose rules disable every maintenance signal
- [ ] 9.3 Document the table, the NULL-inherits convention, the `0` sentinel, the `embed`/`default_search` split and its non-retroactivity, `write_mode` semantics, that policy is instance-wide and admin-only, and that raw SQL editing is unsupported
- [ ] 9.4 `gofmt -w .` and `golangci-lint run` clean; full test suite green

## 10. put_section

- [ ] 10.1 `PutSectionInput` (`category`, `subcategory`, `slug`, `heading`, `content`, `tenant_id`) and the `put_section` MCP tool
- [ ] 10.2 Implement it as a thin call into the shared merge path with a one-section payload — NOT a parallel write implementation, so identity validation, the `embed` decision, `chain_previous`, history and audit all apply
- [ ] 10.3 Creates the document when the path has none; upserts by heading when it exists
- [ ] 10.4 Accept it for `replace`-mode doc_types (a section write is not a document store); reject an existing heading for `append_only`; skip the duplicate guard entirely, since it compares whole-document centroids
- [ ] 10.5 REST equivalent under `/api/`, sharing the same service method
- [ ] 10.6 Tool description states when to reach for `put_section` versus `store_memory`, so a caller stops rewriting documents to add one section
- [ ] 10.7 Tests: adds without a prior read; replaces by heading with no duplicate; creates a missing document; rejected by `slug_format` and `subcategory` validation exactly as `store_memory` is; works on a `replace`-mode type without truncating; `append_only` collision rejected; no duplicate-guard block
