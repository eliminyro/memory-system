## Context

Doc_type behavior is spread across three compiled-in maps and two literal comparisons:

- `episodicDocTypes` (`journal`, `handoff`) — exempt from duplicate guard, staleness, lint stale
  check, cleanup scan.
- `neverPruneDocTypes` (`handoff`) — permanent.
- `DefaultStalenessThresholds` — the seed for the one thing that *is* already a table.
- `InferDocType` — a category switch, the only thing that mints a doc_type. `store_memory` does not
  accept one, and `ValidDocTypes` is referenced nowhere but its own definition and a test, so it
  documents the set rather than guarding it. `Document.Category` has no allowlist or CHECK
  constraint, so categories are already open while doc_types are closed.
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
- A new behavior is a registered rule key, not a patch to four files.
- A misconfigured row is visible rather than silent.

**Non-Goals:**

- Category→doc_type inference. `InferDocType` stays in Go (see D4).
- Cross-tenant read scope. Stays compiled-in (see D5), and it is not a rule key either.
- Per-document overrides. Policy is per doc_type; `Pinned` already exists for the one per-document
  case.
- New behavior of any kind. This change adds no capability an operator did not already have in Go.

## Decisions

**D1 — Widen `staleness_thresholds` in place; do not add a second table.**

Migrate to `doc_type_policies`, replacing `days` with `rules`. The day threshold and the rest of a
type's behavior govern the same doc_type, and splitting them across two tables invites exactly the
drift this project already hit between `preferences/workflow` in memory and `workflow.md` on disk.

*Alternative considered:* a new `doc_type_policies` table alongside the existing one, leaving
`staleness_thresholds` as the day source. Avoids a rename in a public schema, but then two rows
describe one doc_type.

**D2 — `staleness_days = 0` means "never", replacing the episodic exemption.**

Today `journal` (10) and `handoff` (3650) have thresholds that are never read, because `IsEpisodic`
short-circuits first. Encoding "never" as `0` (or an absent key) removes the dead numbers and lets one
rule express what previously took a column plus a map.

*Alternative considered:* a separate `staleness_check` rule alongside the day count. A boolean plus a
number can disagree; `0` cannot.

**D3 — A type's whole behavior is one validated rule set, not typed columns.**

(Rule keys: `staleness_days`, `duplicate_guard`, `cleanup_scan`, `lint_stale_check`, `embed`,
`default_search`, `prunable`, `chain_previous`. `embed` and `default_search` are deliberately separate —
see the Risks section for why one key cannot cover both.)

`doc_type_policies(doc_type TEXT PRIMARY KEY, rules JSONB NOT NULL DEFAULT '{}')`. A new behavior is a
new registered key plus the Go that reads it — no migration, no column that is meaningless for seven
of eight rows.

**This reverses an earlier decision in this document**, which argued for typed columns on the grounds
that SQL predicates in `lint.go` and `section.go` would otherwise have to read through JSON operators.
That was wrong, verified in source: nothing in SQL reads `staleness_thresholds` except its own GORM
SELECT and the seed INSERT (`internal/database/database.go:252`). Threshold values reach search as a Go
`map[string]int` (`internal/repository/section.go:80`) and are applied in `fuseHybrid` post-fusion
(`section.go:451`); `lint.go` binds a doc_type array computed in Go; the table is 8 rows and already
cached by `staleness.ThresholdStore` with an `All()` refresh (`internal/service/memory.go:803`). No
consumer needs a typed column, because no consumer reads the table from SQL.

The guardrail is what makes a schemaless column safe: writes are validated against a registry of
implemented keys and shapes, and an unregistered key is rejected rather than stored. Without that, a
misspelled key stores fine, is read by nothing, and errors nowhere. The registry is also readable, so
a valid row can be written without reading source.

What this does *not* buy, and the docs should not imply it does: a user cannot invent behavior. Only
keys the server implements do anything.

*Alternative considered:* a typed column per switch plus a `behavior` JSONB for the odd ones out.
Self-documenting schema and queryable without JSON operators — neither of which anything here needs —
in exchange for a migration per new behavior.

*Alternative considered:* `staleness_days` as a typed column with the rest in JSONB, since it is the
one value that exists as a column today. Rejected for the same reason: it is read through
`ThresholdStore`, never from SQL, so the column buys nothing and splits one type's behavior across two
representations.

**D4 — Classification stays entirely in Go; only behavior becomes data.**

`InferDocType` is untouched — the category switch and the `projects` slug rules both stay. Deriving the
doc_type from the category is what keeps categories free-form while still giving a place to attach
behavior, and it changes rarely enough that a Go line is the right cost.

The consequence, accepted rather than solved: a category the switch does not recognize lands on
`reference` and its 90-day clock, and adding a genuinely new doc_type means editing the switch. The
verified facts in the Context section still describe the shape of that — categories are open, doc_types
are closed, `store_memory` does not accept one — but they are the design, not a defect.

The split is: *what kind of document is this* stays code; *how is this kind maintained* becomes data.

*Alternative considered, and briefly specced:* a `category_doc_types(category, doc_type)` mapping
table, so adding a category with its own behavior needed no code. Rejected — it is complexity that
breeds rigidity for a derivation that changes once in a long while, and it adds a second table that
must agree with both `InferDocType`'s slug rules and the policy table.

*Alternative considered:* key policy on `(tenant, category)` and drop the doc_type indirection, on the
theory that policy should attach to what users control. It flattens `projects` — one category holding
three doc_types at 14, 30 and 90 days, which is 102 of the ~240 documents in the reference instance.

*Alternative considered:* let the writer declare the doc_type on `store_memory`, making the derivation
optional. Rejected by the user: derivation from the category is the intended design, not a workaround.

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
curation". Where SQL needs a doc_type array (`doc_type <> ALL(?)` in `lint.go`), the array is computed
in Go from the cached rule sets instead.

## Risks / Trade-offs

- **A wrong row silently disables a guard, with no compile-time check.** → Validation on write,
  effective-policy inspection through the admin surface, and a `lint_memory` finding for a doc_type
  whose policy disables every maintenance signal.
- **An unrecognized category still gets `reference`/90d, silently.** Unchanged from today, and now a
  deliberate non-goal rather than a gap (D4). → Nothing to mitigate in this change; it is the accepted
  cost of keeping derivation in Go. Revisit only if a self-hoster asks for their own doc_type without
  touching code.
- **A schemaless column accepts typos.** `{"chain_previos": true}` stores fine, is read by nothing,
  and errors nowhere. → The registry rejects unregistered keys and wrong value shapes on write, and
  exposes the accepted keys so a row can be written without reading the source. This is the one place
  where the flexibility would otherwise become a footgun.
- **Behavior-preserving refactors are where regressions hide.** → The seeded default table in the spec
  is the test oracle: assert each of the 8 doc_types resolves to the rule set that reproduces current
  behavior, and keep the existing episodic tests, retargeted at the rules.
- **Renaming a table and dropping a column in a public schema is a visible migration.** → One
  migration adds `rules`, backfills, drops `days`, renames; older code reading
  `staleness_thresholds.days` breaks, which is why this ships as its own release rather than riding
  along with a feature.
- **`0` as a sentinel for "never" reads as a typo.** → Negative values are rejected, and the registry
  description states the sentinel. Alternative was a second boolean that can contradict the number.
- **`embed: false` means the data has no embeddings, so flipping it true later does not retroactively
  embed anything.** → State it in the registry description; a re-store or re-embed pass is required.
- **`embed: false` alone does not hide a document from search.** The semantic arm requires
  `s.embedding IS NOT NULL`, but the keyword arm matches `s.tsv` with no such condition, so an
  unembedded document still surfaces lexically. → `default_search` is a separate rule and adds a
  predicate to *both* arms; validation rejects `default_search: true` with `embed: false`.
- **Dropping `journal` and `handoff` out of default search is a real behavior change, inside a change
  otherwise sold as behavior-preserving.** → Called out explicitly in the proposal and given its own
  spec scenario, so the test oracle asserts seven-eighths reproduction plus one intended difference
  rather than blanket equivalence.
- **Deleting the predicates touches 8 call sites in one change.** → All eight are in the four files
  named in the proposal, and every one has an existing test.

## Migration Plan

0. `document-listing` ships first. It adds `slug_prefix` / `order_by` / `order` / `limit` to
   `list_documents`, which is what makes `journal`'s `default_search: false` safe — without it,
   hiding journals from unfiltered search leaves only exact-slug fetch or list-everything.
1. One migration: add `rules` to `staleness_thresholds`, backfill each row from the seed table in the
   spec (carrying the existing `days` value into `staleness_days` for the 6 non-episodic types, `0`
   for `journal` and `handoff`), drop `days`, then rename to `doc_type_policies`.
2. Extend the seed loop so a fresh install writes full rule sets, still `ON CONFLICT DO NOTHING`.
3. Replace the 8 call sites; delete the maps and predicates.
4. Ship as its own release before `prompts-category`.

Rollback: the rename and the dropped `days` column are the breaking steps. Rolling back means
renaming the table and restoring `days` from `rules->>'staleness_days'`, so take a snapshot before
migrating.

## Open Questions

- **Per-tenant verification ages.** They do not exist today: `staleness_thresholds` is keyed on
  doc_type alone, so ages are per-type and instance-wide (`learnings` 180d, `projects` 14/30/90d,
  `preferences` 365d, `tools` 90d). Proceeding with that spread unchanged. Per-tenant ages would be a
  separate change adding a tenant dimension to the key, and would have to layer over the per-type
  spread rather than replace it.
- Does `prunable` earn a rule key, given nothing implements retention or eviction (the cleanup scanner
  only enqueues pairs and prunes `mutation_history`; `ArchiveByID` has one caller,
  `link_documents ... supersedes`)? Seeding it documents the intent, but it is a rule nothing reads.
- Should the admin surface expose policy editing, or is operator-edits-the-table via SQL enough for
  the first cut?
- **Per-document expiry** (`documents.expires_at`, nullable, filtered on read) for transient
  knowledge. Raised alongside this design, but it is per-document rather than per-type, so it is out of
  scope here. Confirm whether it belongs in this change or its own.
