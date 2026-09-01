## Why

How a document is maintained — whether its verification clock runs, whether the duplicate guard
fires, whether the cleanup scanner considers it, whether it is searchable, whether it can be pruned —
is decided by three hardcoded maps in `internal/models/staleness.go` plus two hardcoded
`doc_type == handoff` checks. Every new kind of document, and every change to how an existing kind is
treated, means editing Go and cutting a release.

`staleness_thresholds` already proves the better shape: a table keyed by doc_type, seeded
`ON CONFLICT DO NOTHING` so tweaked rows survive upgrades, read through a cached store. It just
expresses one thing — a day count — when the doc_type actually carries a whole bundle of behavior.

Deriving the doc_type from the category stays exactly as it is. That derivation is what keeps
categories free-form while still giving a place to attach behavior; it is not the thing that needs
changing. What needs changing is how the behavior attached to a type is defined.

## What Changes

- `staleness_thresholds` becomes `doc_type_policies(doc_type TEXT PRIMARY KEY, rules JSONB NOT NULL
  DEFAULT '{}')`. One row per doc_type, and the row holds the type's whole behavior as a validated
  rule set rather than a day count.
- Registered rule keys: `staleness_days` (0 or absent = the clock never runs), `duplicate_guard`,
  `cleanup_scan`, `lint_stale_check`, `embed`, `default_search`, `prunable`, and `chain_previous` for
  the one behavior only handoff has today.
- Writes are validated against a registry of implemented keys and their accepted value shapes. An
  unregistered key or a wrong shape is rejected, never silently stored, and the registry is readable
  so a valid row can be written without reading source.
- `embed` and `default_search` are separate rules. The semantic arm of the search query already skips
  sections with no embedding, but the keyword arm does not, so hiding a doc_type from default results
  needs its own predicate on both arms. Keeping them separate is what lets `journal` stay searchable
  on request while dropping out of unfiltered results.
- `episodicDocTypes`, `neverPruneDocTypes`, and their four predicates (`IsEpisodic`,
  `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes`) are replaced by rule lookups
  at their 6 call sites.
- The two hardcoded handoff checks (`internal/repository/document.go:158`,
  `internal/service/memory.go:1255`) read the `chain_previous` rule instead of comparing doc_type to
  a literal.
- Seeded rows reproduce today's behavior exactly for all 8 doc_types, with **one deliberate
  exception**: `journal` and `handoff` get `default_search: false`. Nothing excludes them from
  unfiltered results today, so an unfiltered query can return a journal entry ahead of the knowledge
  document that answers it. They keep `embed: true`, so asking for them by category or doc_type still
  works with full semantic ranking. Everything else about the change is behavior-preserving.

Explicitly unchanged:

- **`InferDocType` and the whole category→doc_type derivation.** The Go switch stays, `projects` slug
  rules included. Adding a doc_type still means a line there. A category the switch does not
  recognize still lands on `reference` and its 90-day clock — accepted behavior, not a problem this
  change solves.
- **Policy stays keyed on doc_type, not category.** `projects` is one category holding three
  doc_types at 14, 30 and 90 days; keying on category would flatten the largest category in the
  reference instance.
- **Journal and handoff stay embedded and findable.** They drop out of *unfiltered* results only;
  a query naming their category or doc_type still ranks them semantically, and `resume()` walks handoff
  chains untouched.
- **Cross-tenant read scope.** A trust boundary, not a maintenance preference, and not a rule key.

## Capabilities

### New Capabilities

- `doc-type-policy`: the `doc_type_policies` table, its rule keys and their effect on each curation
  mechanism, the registry and its validation, seeding, resolution with the `reference` fallback, and
  inspection.

### Modified Capabilities

None. `openspec/specs/` holds no spec files yet, so the behaviors this refactors — staleness, lint,
cleanup scan, duplicate guard, search visibility, handoff chaining — have nothing to delta against.
Their current behavior is captured as the seeded defaults in the spec, which is what the tests assert.

## Impact

- `internal/models/staleness.go` — the three maps and four predicates go away; `DefaultStalenessThresholds` becomes the default rule set. `InferDocType` is untouched.
- `internal/models/` — new `DocTypePolicy` model carrying `rules`.
- `internal/database/database.go:249` — migration adding `rules`, backfilling from the default set, dropping `days`, renaming the table; seed loop writes rule sets.
- `internal/staleness/staleness.go` — `ThresholdStore` becomes the policy store; `DaysFor` and `Check` read `staleness_days` from the rule set.
- `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`.
- `internal/service/memory.go:1255`, `internal/repository/document.go:158` — chaining reads `chain_previous`.
- `internal/service/staleness_view.go` — two `IsEpisodic` calls become rule reads.
- `internal/repository/lint.go` — three `EpisodicDocTypes()` bindings become rule-derived doc_type arrays.
- `internal/repository/section.go` — both arms of the search candidate query gain the `default_search` predicate.
- Embedding write path — skipped for a type whose `embed` is false.
- Admin surface — read the policy table and the rule registry.

Data: `staleness_thresholds` is migrated in place to `doc_type_policies` with rule sets that match
current behavior. No document rows touched.

Callers: none. No tool signature changes.
