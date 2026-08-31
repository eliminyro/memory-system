## Context

Doc_type behavior is spread across three compiled-in maps and two literal comparisons:

- `episodicDocTypes` (`journal`, `handoff`) — exempt from duplicate guard, staleness, lint stale
  check, cleanup scan.
- `neverPruneDocTypes` (`handoff`) — permanent.
- `DefaultStalenessThresholds` — the seed for the one thing that *is* already a table.
- `internal/repository/document.go:158` and `internal/service/memory.go:1255` — `doc_type == handoff`
  drives handoff chaining.

Call sites reading the predicates, verified on this branch: `internal/repository/lint.go` (3
`EpisodicDocTypes()` bindings), `internal/service/memory.go:1123` (duplicate guard),
`internal/service/staleness_view.go` (2 `IsEpisodic` calls). Six in total, plus the two chaining
comparisons.

`staleness_thresholds` is already the shape this change wants: global, keyed by doc_type, seeded
`ON CONFLICT DO NOTHING` with a comment saying the seed must not overwrite operator edits, and read
through a cached `ThresholdStore` with a `DaysFor` fallback to `reference`. `internal/globalconfig`
holds the instance singleton with a live snapshot — the precedent for instance-wide settings that an
operator edits and every tenant reads.

## Goals / Non-Goals

**Goals:**

- Doc_type maintenance behavior is data, editable by the operator without a release.
- The 8 existing doc_types keep behaving exactly as they do now.
- A new doc_type is a row, not a patch to four files.
- A misconfigured row is visible rather than silent.

**Non-Goals:**

- Category→doc_type inference. `InferDocType` stays in Go (see D4).
- Cross-tenant read scope. Stays compiled-in (see D5), and it is not a `behavior` key either.
- Per-document overrides. Policy is per doc_type; `Pinned` already exists for the one per-document
  case.
- New behavior of any kind. This change adds no capability an operator did not already have in Go.

## Decisions

**D1 — Widen `staleness_thresholds` in place; do not add a second table.**

Rename to `doc_type_policies` and add the flag columns. The day threshold and the flags govern the
same doc_type, and splitting them across two tables invites exactly the drift this project already
hit between `preferences/workflow` in memory and `workflow.md` on disk.

*Alternative considered:* a new `doc_type_policies` table alongside the existing one, leaving
`staleness_thresholds` as the day source. Avoids a rename in a public schema, but then two rows
describe one doc_type.

**D2 — `staleness_days = 0` means "never", replacing the episodic exemption.**

Today `journal` (10) and `handoff` (3650) have thresholds that are never read, because `IsEpisodic`
short-circuits first. Encoding "never" as `0` removes the dead numbers and lets one column express
what previously took a column plus a map.

*Alternative considered:* a separate `staleness_check bool`. A boolean plus a day count can disagree,
and `0` cannot.

**D3 — Universal switches get columns; subset behaviors get a `behavior` JSONB.**

A column is justified when every doc_type answers it meaningfully. `staleness_days`,
`duplicate_guard`, `cleanup_scan`, `lint_stale_check`, `prunable`, and `search_default_visible` all do.
Handoff chaining does not — it would be `false` in seven of eight rows, and the next subset behavior
adds another mostly-false column.

`behavior JSONB NOT NULL DEFAULT '{}'` holds those, validated against a registry of implemented keys.
The second gain is parameterization: a key accepts `true` or an object, so chaining can grow from
`{"chain_previous": true}` to `{"chain_previous": {"scope": "subcategory", "link_type":
"continues_from"}}` with no migration.

What this does *not* buy, and the docs should not imply it does: a user cannot invent behavior. Only
keys the server implements do anything, which is exactly why unregistered keys are rejected rather
than stored.

*Alternative considered:* a boolean column per behavior. Honest schema, self-documenting, queryable
without JSON operators — and a new mostly-false column per behavior forever.

*Alternative considered:* JSONB for everything, no typed columns. Uniform, but it throws away NOT NULL
and type checks on the six switches that genuinely are universal, and makes the SQL predicates in
`lint.go` and `section.go` read through JSON operators for no reason.

**D4 — Classification stays in Go, behavior becomes data.**

`InferDocType` is rule-based: `projects` + slug `state` → `project_state`, `projects` + slug
containing audit/plan/design/backlog → `audit`, everything else by category. Expressing that in config
means a pattern language, a matcher, precedence rules, and a validator — a config rule engine, to
save editing one switch statement that changes once a year.

The split is: *what kind of document is this* stays code; *how is this kind maintained* becomes data.

**D5 — Cross-tenant read scope stays compiled-in.**

`readableTenants` always adds the home tenant plus `models.BootstrapTenantID` (the common pool), then
every viewer-granted tenant. A `cross_tenant_readable` flag would let a row edit decide whether one
tenant's documents can be read by another. For instruction-shaped doc_types that means another
tenant's text becoming an agent's operating rules.

If it is ever wanted it is additively one column and one call site, and it would be instance-admin
only — never per-tenant self-service, which is why `UpdateMyTenantSettings` already requires manager
for the toggles that arm destructive behavior.

**D6 — Instance-wide rows; no per-tenant overrides.**

One row per doc_type, applying to every tenant. Overrides would need their own table — the policy
table's primary key is `doc_type`, so a per-tenant row has nowhere to go — plus a resolution layer and
an inspection surface reporting which value came from where. Nothing needs that: the operator of a
self-hosted instance edits the global row, and the per-tenant knobs that do exist
(`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled` on `tenants`) already gate *whether* a
mechanism runs. The policy only decides *which doc_types* it applies to once it does, so the two
compose without overlapping.

*Alternative considered:* `doc_type_policy_overrides(tenant_id, doc_type, ...)` with nullable columns,
resolved COALESCE-style like `effectiveDuplicateThreshold`. Additive later if a shared tenant ever
needs different maintenance from its host instance.

*Alternative considered:* one table with `tenant_id UUID NULL` for the global row. Fewer tables, but
NULLs are distinct in a unique constraint, so nothing stops two global rows for one doc_type without a
partial unique index — and which row is authoritative becomes a runtime question instead of a schema
one.

**D7 — `ThresholdStore` becomes the policy store, keeping its cache.**

It already caches doc_type→days with an `All()` refresh and is already threaded to `Check`. Widening
the cached value from an int to a policy struct reuses the invalidation and the call graph instead of
introducing a parallel accessor.

**D8 — Predicates are deleted, not kept as wrappers.**

`IsEpisodic`, `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes` go away rather than
becoming policy-backed shims. Keeping them would preserve "episodic" as a concept the schema no
longer has, and the term is already doing double duty for "append-per-day" and "exempt from
curation". Where SQL needs a doc_type array (`doc_type <> ALL(?)` in `lint.go`), the array is derived
from the policy rows instead.

## Risks / Trade-offs

- **A wrong row silently disables a guard, with no compile-time check.** → Validation on write,
  effective-policy inspection through the admin surface, and a `lint_memory` finding for a doc_type
  whose policy disables every maintenance signal.
- **A schemaless column accepts typos.** `{"chain_previos": true}` stores fine, is read by nothing,
  and errors nowhere. → The registry rejects unregistered keys and wrong value shapes on write, and
  exposes the accepted keys so a row can be written without reading the source. This is the one place
  where the flexibility would otherwise become a footgun.
- **Behavior-preserving refactors are where regressions hide.** → The seeded default table in the spec
  is the test oracle: assert each of the 8 doc_types resolves to the flag set that reproduces current
  behavior, and keep the existing episodic tests, retargeted at the policy.
- **Renaming a table in a public schema is a visible migration.** → Additive column widening plus a
  rename in one migration; older code reading `staleness_thresholds` breaks, which is why this ships
  as its own release rather than riding along with a feature.
- **`0` as a sentinel for "never" reads as a typo.** → Rejected negative values, and the column comment
  plus the tool description state the sentinel. Alternative was a second boolean that can contradict
  the number.
- **Deleting the predicates touches 8 call sites in one change.** → All eight are in the four files
  named in the proposal, and every one has an existing test.

## Migration Plan

1. One migration: add the flag columns to `staleness_thresholds`, backfill them from the seed table in
   the spec, then rename to `doc_type_policies`. Existing `days` values for the 6 non-episodic types
   are preserved; `journal` and `handoff` are set to `0`.
2. Extend the seed loop so a fresh install writes full rows, still `ON CONFLICT DO NOTHING`.
3. Replace the 8 call sites; delete the maps and predicates.
4. Ship as its own release before `prompts-category`.

Rollback: the rename is the breaking step. Roll back by renaming the table back — the extra columns
are inert to older code, which reads only `doc_type` and `days`.

## Open Questions

- Does `prunable` earn a column now, given nothing implements retention or eviction (the cleanup
  scanner only enqueues pairs and prunes `mutation_history`; `ArchiveByID` has one caller,
  `link_documents ... supersedes`)? Seeding it documents the intent, but it is a flag nothing reads.
- Should the admin surface expose policy editing, or is operator-edits-the-table via SQL enough for
  the first cut?
