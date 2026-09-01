## 1. Model and migration

- [ ] 1.1 Add the `DocTypePolicy` model in `internal/models/` — `doc_type` primary key plus `rules` (JSONB, NOT NULL DEFAULT `{}`), with a typed Go struct for the rule set so callers do not index a raw map
- [ ] 1.2 Replace `DefaultStalenessThresholds` with the default rule sets from the spec's seed table: `chain_previous` on `handoff`, `prunable: false` on `handoff`, `staleness_days: 0` on `journal`/`handoff`, `embed: true` on all 8, and `default_search: false` on `journal`/`handoff` (the one intended behavior change)
- [ ] 1.3 Migration in `internal/database/database.go`: add `rules`, backfill each row (existing `days` → `staleness_days`, `0` for `journal`/`handoff`), drop `days`, rename the table to `doc_type_policies`
- [ ] 1.4 Extend the seed loop to write full rule sets, keeping `ON CONFLICT DO NOTHING` so operator edits survive
- [ ] 1.5 Test: every member of `ValidDocTypes` has a seeded row; a doc_type with no row falls back to `reference`; the migration carries existing `days` values through unchanged

## 2. Rule registry and validation

- [ ] 2.1 Registry of implemented rule keys with the value shape each accepts: `staleness_days` (int ≥ 0), `duplicate_guard`, `cleanup_scan`, `lint_stale_check`, `embed`, `default_search`, `prunable` (bool), `chain_previous` (bool or parameter object)
- [ ] 2.2 Validation on policy write: unknown doc_type, unregistered key, wrong value shape, negative `staleness_days`, `default_search: true` with `embed: false`, and a rule set leaving a doc_type both non-prunable and invisible to every read path
- [ ] 2.3 Expose the registry (keys + accepted shapes + the `0`-means-never sentinel + the note that flipping `embed` true does not retroactively embed) through the policy read surface
- [ ] 2.4 Tests: unregistered key rejected by name; wrong shape rejected; `{}` valid and falling back to `reference`; registry listing returns the keys

## 3. Policy store

- [ ] 3.1 Widen `staleness.ThresholdStore` into the policy store — cached value becomes the rule-set struct, keeping the existing `All()` refresh and `reference` fallback
- [ ] 3.2 Resolution: the doc_type's row, else the `reference` row; a key absent from a row falls back to the `reference` row's value for that key. Instance-wide, no per-tenant layer
- [ ] 3.3 Tests: unknown doc_type falls back to `reference`; a partial rule set falls back per key; a tenant toggle (`cleanup_scan_enabled = false`) still suppresses the mechanism regardless of the doc_type's rule

## 4. Replace the call sites

- [ ] 4.1 `internal/staleness/staleness.go` — `Check` skips evaluation when `staleness_days` is `0` or absent (neither stale nor guarded)
- [ ] 4.2 `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`
- [ ] 4.3 `internal/service/staleness_view.go` — both `IsEpisodic` calls read the rule set
- [ ] 4.4 `internal/repository/lint.go` — the 3 `EpisodicDocTypes()` bindings become doc_type arrays computed in Go from the cached rule sets (`lint_stale_check` for the stale check, `cleanup_scan` for the scanner query)
- [ ] 4.5 `internal/repository/document.go:158` and `internal/service/memory.go:1255` — chaining reads `chain_previous` instead of comparing to `DocTypeHandoff`
- [ ] 4.6 Assert nothing removes a document whose doc_type has `prunable` false (nothing implements retention today — the test guards a future sweep)
- [ ] 4.7 Delete `episodicDocTypes`, `neverPruneDocTypes`, `IsEpisodic`, `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes`. `InferDocType` and `ValidDocTypes` stay untouched

## 5. Embedding and default search visibility

- [ ] 5.1 Skip embedding generation and storage on write when the doc_type's `embed` is false
- [ ] 5.2 `internal/repository/section.go` — add the `default_search` exclusion to BOTH arms of the candidate query (the semantic arm and the `s.tsv` keyword arm), suppressed when the caller named a category or doc_type
- [ ] 5.3 Validation: reject `default_search: true` together with `embed: false`
- [ ] 5.4 Confirm `get_document` and `list_documents` still return excluded documents unchanged
- [ ] 5.5 Tests: no embedding rows for `embed: false`; an `embed: true` + `default_search: false` doc_type is absent from both arms of an unfiltered query but ranks semantically under an explicit filter; an `embed: false` + `default_search: false` doc_type does not leak through the keyword arm; contradictory rule set rejected

## 6. Behavior-preservation tests

- [ ] 6.1 Table test asserting each of the 8 doc_types resolves to the seed rule set from the spec
- [ ] 6.2 Retarget the existing episodic tests (`TestIsEpisodic` and the curation-exemption tests) at the rule set, keeping their assertions
- [ ] 6.3 Journal and handoff: stale-but-served, no duplicate guard, no cleanup pair, no lint stale finding, **still embedded**, and still semantically rankable under an explicit filter — same as before the refactor except absence from unfiltered results
- [ ] 6.4 Handoff chaining still links to the prior latest handoff; `resume()` still walks the chain; removing the rule stops the chaining
- [ ] 6.5 Configuration-driven cases: `duplicate_guard` off for `learning` lets a near-duplicate through; `default_search` off hides a doc_type from unfiltered search but not from `get_document` or a filtered search

## 7. Surface and docs

- [ ] 7.1 Admin read of the policy table, including doc_types falling back to the `reference` row
- [ ] 7.2 `lint_memory` finding for a doc_type whose rule set disables every maintenance signal
- [ ] 7.3 Document the table, the rule keys, the `0` sentinel, the `embed` backfill caveat, the `embed`/`default_search` split, and that policy is instance-wide; state plainly that only implemented keys do anything and that adding a doc_type is still a code change
- [ ] 7.4 `gofmt -w .` and `golangci-lint run` clean; full test suite green
