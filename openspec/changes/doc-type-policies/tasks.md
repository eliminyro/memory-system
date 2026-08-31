## 1. Model and migration

- [ ] 1.1 Add the `DocTypePolicy` model in `internal/models/` — doc_type, staleness_days, duplicate_guard, cleanup_scan, lint_stale_check, prunable, search_default_visible, behavior (JSONB, NOT NULL DEFAULT `{}`)
- [ ] 1.2 Replace `DefaultStalenessThresholds` with the default policy row set from the spec's seed table, including `{"chain_previous": true}` on `handoff` and `staleness_days = 0` on `journal`/`handoff`
- [ ] 1.3 Migration in `internal/database/database.go`: add the columns to `staleness_thresholds`, backfill from the default set, rename the table to `doc_type_policies`
- [ ] 1.4 Extend the seed loop to write full rows, keeping `ON CONFLICT DO NOTHING` so operator edits survive
- [ ] 1.5 Test: every member of `ValidDocTypes` has a seeded row; a missing row falls back to `reference`

## 2. Behavior registry

- [ ] 2.1 Registry of implemented `behavior` keys with the value shape each accepts; `chain_previous` accepts `true` or a parameter object
- [ ] 2.2 Validation on policy write: unknown doc_type, negative `staleness_days`, unregistered behavior key, wrong value shape, and a row both non-prunable and invisible to every read path
- [ ] 2.3 Expose the registry (keys + accepted shapes) through the policy read surface
- [ ] 2.4 Tests: unregistered key rejected by name; wrong shape rejected; `{}` valid; registry listing returns the keys

## 3. Policy store

- [ ] 3.1 Widen `staleness.ThresholdStore` into the policy store — cached value becomes the policy struct, keeping the existing `All()` refresh and `reference` fallback
- [ ] 3.2 Resolution: the doc_type's row, else the `reference` row. Instance-wide, no per-tenant layer
- [ ] 3.3 Tests: unknown doc_type falls back to `reference`; a tenant toggle (`cleanup_scan_enabled = false`) still suppresses the mechanism regardless of the doc_type's flag

## 4. Replace the call sites

- [ ] 4.1 `internal/staleness/staleness.go` — `Check` skips evaluation when `staleness_days = 0` (neither stale nor guarded)
- [ ] 4.2 `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`
- [ ] 4.3 `internal/service/staleness_view.go` — both `IsEpisodic` calls read the policy
- [ ] 4.4 `internal/repository/lint.go` — the 3 `EpisodicDocTypes()` bindings become policy-derived doc_type arrays (`lint_stale_check` for the stale check, `cleanup_scan` for the scanner query)
- [ ] 4.5 `internal/repository/section.go` — search candidate query gains the `search_default_visible` predicate, suppressed when the caller named a category or doc_type
- [ ] 4.6 `internal/repository/document.go:158` and `internal/service/memory.go:1255` — chaining reads the `chain_previous` behavior key instead of comparing to `DocTypeHandoff`
- [ ] 4.7 Assert nothing removes a document whose doc_type has `prunable` false (nothing implements retention today — the test guards a future sweep)
- [ ] 4.8 Delete `episodicDocTypes`, `neverPruneDocTypes`, `IsEpisodic`, `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes`

## 5. Behavior-preservation tests

- [ ] 5.1 Table test asserting each of the 8 doc_types resolves to the seed row from the spec
- [ ] 5.2 Retarget the existing episodic tests (`TestIsEpisodic` and the curation-exemption tests) at the policy, keeping their assertions
- [ ] 5.3 Journal and handoff: stale-but-served, no duplicate guard, no cleanup pair, no lint stale finding — same outcomes as before the refactor
- [ ] 5.4 Handoff chaining still links to the prior latest handoff; removing the key stops it
- [ ] 5.5 Configuration-driven cases: `duplicate_guard` off for `learning` lets a near-duplicate through; `search_default_visible` off hides a doc_type from unfiltered search but not from a filtered one

## 6. Surface and docs

- [ ] 6.1 Admin read of the policy table, including doc_types falling back to the `reference` row
- [ ] 6.2 `lint_memory` finding for a doc_type whose policy disables every maintenance signal
- [ ] 6.3 Document the table, the `0` sentinel, and the registry; state plainly that only implemented keys do anything, and that policy is instance-wide
- [ ] 6.4 `gofmt -w .` and `golangci-lint run` clean; full test suite green
