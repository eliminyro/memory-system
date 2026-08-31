## Why

How a document is maintained — whether its staleness clock runs, whether the duplicate guard fires,
whether the cleanup scanner considers it, whether it can be pruned, whether search shows it by
default — is decided by three hardcoded maps in `internal/models/staleness.go` plus two hardcoded
`doc_type == handoff` checks. Every new document kind means editing Go and a release, and the
compiled-in set encodes one operator's taxonomy into a self-hostable server.

`staleness_thresholds` already proves the better shape: a global, operator-editable table keyed by
doc_type, seeded `ON CONFLICT DO NOTHING` so tweaked rows survive upgrades. It just has one column.
Widening it turns doc_type behavior into data, and every existing special case becomes a seeded row.

This lands before `prompts-category`, which otherwise has to add a fourth hardcoded exemption class
and then have it deleted.

## What Changes

- `staleness_thresholds` widens into `doc_type_policies`: one row per doc_type carrying the curation
  switches that are compiled-in today — `staleness_days`, `duplicate_guard`, `cleanup_scan`,
  `lint_stale_check`, `prunable`, `search_default_visible`.
- A `behavior` JSONB column carries doc_type-specific parameters, for behaviors that apply to some
  doc_types rather than all of them. Writes are validated against a registry of known keys per
  behavior, so an unrecognized key is rejected instead of silently doing nothing.
- `episodicDocTypes`, `neverPruneDocTypes`, and their predicates (`IsEpisodic`,
  `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes`) are replaced by policy
  lookups at their 6 call sites.
- The two hardcoded handoff checks (`internal/repository/document.go:158`,
  `internal/service/memory.go:1255`) read the `chain_previous` behavior key rather than comparing
  doc_type to a literal.
- Policy is instance-wide: one row per doc_type for every tenant. The existing per-tenant toggles
  (`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled`) keep their meaning — they gate whether
  a mechanism runs, the policy decides which doc_types it covers once it does.
- Seeded rows reproduce today's behavior exactly for all 8 existing doc types. **This is a
  behavior-preserving refactor**; no observable change on upgrade.
- Policy rows are validated on write, and the effective table is readable so a misconfiguration is
  visible rather than silent.

Explicitly unchanged:

- **Category→doc_type inference stays in Go.** `InferDocType` is rule-based (`projects/state` →
  `project_state`; slug containing audit/plan/design/backlog → `audit`), and moving it to config means
  shipping a pattern language. Behavior is data; classification stays code.
- **Cross-tenant read scope stays compiled-in.** It is a trust boundary, not a maintenance
  preference, and it does not become a `behavior` key either. A future `cross_tenant_readable` flag is
  additively one column and one call site if it is ever wanted, and would be instance-admin-only.

## Capabilities

### New Capabilities

- `doc-type-policy`: the `doc_type_policies` table, its flags and their effect on each curation
  mechanism, instance-wide resolution with the `reference` fallback, seeding, validation, and
  inspection.

### Modified Capabilities

None. `openspec/specs/` holds no spec files yet, so the behaviors this refactors — staleness, lint,
cleanup scan, duplicate guard, handoff chaining — have nothing to delta against. Their current
behavior is captured as the seeded defaults in `design.md`, which is what the tests assert.

## Impact

- `internal/models/staleness.go` — the three maps and four predicates go away; `DefaultStalenessThresholds` becomes the default policy row set.
- `internal/models/` — new `DocTypePolicy` model.
- `internal/database/database.go:249` — migration widening the table, seed loop extended.
- `internal/staleness/staleness.go` — `ThresholdStore` becomes a policy store; `DaysFor` and `Check` read from the policy row.
- `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`.
- `internal/service/memory.go:1255`, `internal/repository/document.go:158` — chaining reads `chain_previous`.
- `internal/service/staleness_view.go` — two `IsEpisodic` calls become policy reads.
- `internal/repository/lint.go` — three `EpisodicDocTypes()` bindings become policy-derived doc_type arrays.
- `internal/repository/section.go` — search candidate query gains the `search_default_visible` predicate.
- Admin surface — read and edit policy rows.

Data: `staleness_thresholds` is widened in place with defaults that match current behavior. No
document rows touched. Downgrade leaves extra columns older code ignores.

Callers: none. No tool signature changes.
