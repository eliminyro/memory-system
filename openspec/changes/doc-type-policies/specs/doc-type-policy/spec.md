## ADDED Requirements

### Requirement: Doc_type policy table

The system SHALL store one policy row per doc_type in `doc_type_policies`, the migrated
`staleness_thresholds` table, replacing the compiled-in `episodicDocTypes` and `neverPruneDocTypes`
maps.

Scalar rules SHALL be nullable typed columns, where NULL means "inherit from the `reference` row".
NULL must stay distinguishable from a set value, because `staleness_days = 0` means "never check" and
would otherwise collide with an absent value.

| Column | Type | Meaning |
| --- | --- | --- |
| `doc_type` | text, PK | |
| `staleness_days` | int | Verification threshold. `0` means the clock never runs |
| `duplicate_guard` | bool | Write-time near-duplicate check evaluates this doc_type |
| `cleanup_scan` | bool | Near-duplicate scanner considers this doc_type |
| `lint_stale_check` | bool | `lint_memory` reports this doc_type as stale |
| `embed` | bool | Sections get embeddings generated on write |
| `default_search` | bool | Appears in `search_memory` when the caller names no `category` or `doc_type` |
| `prunable` | bool | Retention or eviction may ever remove documents of this doc_type |
| `write_mode` | enum | `replace` \| `merge_sections` \| `append_only` |
| `slug_format` | enum | `any` \| `date` \| `datetime` \| `kebab` |
| `subcategory` | enum | `optional` \| `required` \| `forbidden` |
| `rules` | jsonb, NOT NULL DEFAULT `{}` | Structured and experimental rules |

`rules` SHALL be constrained only to "must be a JSON object". It holds rules whose value is not a
scalar — `chain_previous` today — and serves as the place to try a new rule without a migration.
Graduating a rule to a typed column is one migration, shipped with the Go that reads it.

A policy row SHALL exist for every member of `ValidDocTypes`, which remains the authority on what
doc_types exist: `InferDocType` is still the only thing that mints one.

#### Scenario: Every valid doc_type has a policy

- **WHEN** the server finishes migrating
- **THEN** a policy row exists for every member of `ValidDocTypes`

#### Scenario: NULL inherits, zero does not

- **WHEN** one row has `staleness_days` NULL and another has `staleness_days` `0`
- **THEN** the first inherits the `reference` row's threshold and the second never runs the clock

#### Scenario: Unknown doc_type falls back

- **WHEN** a policy is requested for a doc_type with no row
- **THEN** the `reference` row's effective rules are returned

### Requirement: Rules load at boot and recompute on admin write

The effective rule set SHALL be built once at startup: load every row, resolve NULLs against the
`reference` row, validate the merged result, and hold it in memory. A row that fails validation SHALL
stop the server, naming the doc_type and the field.

The admin surface SHALL recompute the in-memory set in process when it writes a rule, so a change takes
effect immediately with no restart and no polling. Between boot and an admin write the in-memory set is
authoritative, and every rule read is an in-memory lookup.

The system SHALL NOT poll the table on a timer. The 5-minute TTL that `staleness.ThresholdStore` uses
today is removed: rules do not change within a session, and far more than five minutes passes between
sessions, so the timer pays a periodic query for a staleness window nothing benefits from.

#### Scenario: Invalid row stops startup

- **WHEN** the server starts and a policy row fails validation
- **THEN** startup fails with an error naming the doc_type and the offending field

#### Scenario: Admin edit takes effect immediately

- **WHEN** an admin changes a rule through the admin surface
- **THEN** the next request uses the new value, with no restart and no waiting

#### Scenario: No periodic reload

- **WHEN** the server runs for an extended period with no admin writes
- **THEN** it issues no repeated queries against the policy table

### Requirement: Rules are edited only by instance admins

Policy rows are instance-wide and affect every tenant, so writing one SHALL require instance admin —
not tenant manager, which gates the per-tenant toggles. Every write SHALL be audited to `override_log`,
matching `UpdateMyTenantSettings`.

Editing rows with raw SQL is unsupported: it bypasses both validation and in-process recomputation.
Because the admin surface is the only sanctioned write path, Go-side validation before the write is the
primary guard, and database `CHECK` constraints are defense-in-depth.

`CHECK` constraints SHALL be several narrowly named constraints rather than one combined expression — a
violation reports only the constraint name, so the name has to be the diagnosis.

#### Scenario: Non-admin rejected

- **WHEN** a tenant manager attempts to write a policy row
- **THEN** the write is refused

#### Scenario: Write is audited

- **WHEN** an admin changes a rule
- **THEN** an `override_log` entry records the change

#### Scenario: Constraint names diagnose

- **WHEN** a `CHECK` rejects a write
- **THEN** the violated constraint name identifies which rule failed, for example `chk_write_mode_enum`

### Requirement: Write mode controls how sections land

`write_mode` SHALL decide what happens to an existing document's sections when a document is stored at
the same path.

- `replace` — delete all existing sections and write the payload's. Today's behavior, and the default.
- `merge_sections` — an incoming section replaces the existing section with the same heading; an
  incoming heading not already present is added; an existing heading absent from the payload is left
  alone. Idempotent: re-sending identical content changes nothing, which plain appending would not.
- `append_only` — as `merge_sections`, but an incoming heading matching an existing one is an error
  rather than a replace.

This closes a data-loss hole. Today a store to an existing path deletes every section
(`internal/service/memory.go:1192`), so writing one new journal section destroys the day's earlier
entries unless the caller first reads the document and resends it whole.

`duplicate_guard: true` SHALL require `write_mode: replace`. The guard has no defined meaning under a
merge — whether it should compare the merged document's centroid or only the incoming sections are both
defensible — so the combination is rejected rather than resolved arbitrarily.

#### Scenario: Merge preserves earlier sections

- **WHEN** a journal document exists with a `## 09:15` section and a store arrives carrying only `## 14:30`
- **THEN** the document holds both sections afterwards

#### Scenario: Merge replaces a matching heading

- **WHEN** the same document receives a store carrying `## 09:15` with new content
- **THEN** that section's content is replaced and no duplicate section is created

#### Scenario: Merge is idempotent

- **WHEN** identical content is stored twice under `merge_sections`
- **THEN** the document is unchanged after the second store

#### Scenario: Replace still truncates

- **WHEN** a document whose doc_type has `write_mode: replace` is stored
- **THEN** its existing sections are deleted and replaced, exactly as today

#### Scenario: Append-only rejects a collision

- **WHEN** a store under `append_only` carries a heading that already exists
- **THEN** the write is rejected

#### Scenario: Guard and merge are incompatible

- **WHEN** a policy write sets `duplicate_guard: true` with `write_mode: merge_sections`
- **THEN** the write is rejected with a validation error

### Requirement: Identity validation

`slug_format` SHALL validate the slug on write: `date` requires `YYYY-MM-DD`, `datetime` a timestamp,
`kebab` lowercase words separated by hyphens, and `any` accepts anything.

`subcategory` SHALL require, forbid, or allow a subcategory.

Validation SHALL NOT alter the slug. `InferDocType` consumes the slug to classify the document before
rules are loaded, so a changed slug would contradict the classification already made. Server-side slug
*derivation* is out of scope for the same reason, and because no timezone exists anywhere in the models
or config — a server-derived date would be UTC, putting an evening journal entry on tomorrow's document.

#### Scenario: Malformed journal slug rejected

- **WHEN** a document is stored at `journal/sept-1`
- **THEN** the write is rejected, naming the expected format

#### Scenario: Well-formed slug accepted

- **WHEN** a document is stored at `journal/2026-09-01`
- **THEN** the write proceeds

#### Scenario: Missing required subcategory rejected

- **WHEN** a handoff is stored with no subcategory
- **THEN** the write is rejected

#### Scenario: Forbidden subcategory rejected

- **WHEN** a journal is stored with a subcategory
- **THEN** the write is rejected

### Requirement: Curation mechanisms read the rules

Each curation mechanism SHALL consult the doc_type's effective rules rather than a compiled-in set:

- Staleness evaluation SHALL skip a section whose `staleness_days` is `0`, marking it neither stale nor
  guarded, and SHALL never withhold its content under hard staleness mode.
- The write-time duplicate guard SHALL be skipped when `duplicate_guard` is false.
- The near-duplicate scanner SHALL exclude doc_types whose `cleanup_scan` is false.
- `lint_memory`'s stale check SHALL exclude doc_types whose `lint_stale_check` is false.
- Retention and eviction SHALL never remove a document whose `prunable` is false.
- A document whose doc_type carries `chain_previous` SHALL link to the prior latest document in its
  scope, as handoffs do today.

#### Scenario: Turning off a guard by configuration

- **WHEN** an admin sets `duplicate_guard` false for `learning` and a near-identical learning document is written
- **THEN** the write succeeds with no `similar_exists` status and no code change was required

#### Scenario: Chaining is rule-driven

- **WHEN** `chain_previous` is set for a doc_type other than `handoff` and a document of that type is created
- **THEN** it links to the prior latest document of that type in its scope

#### Scenario: Zero-day staleness never guards

- **WHEN** a section's doc_type has `staleness_days: 0`, it was verified years ago, and it mentions a code path
- **THEN** it is returned in full, unguarded, with no `needs_verification` status

### Requirement: Embedding and default search visibility are separate rules

`embed` and `default_search` SHALL be distinct, because the search query treats them differently: the
semantic arm requires `s.embedding IS NOT NULL`, but the keyword arm matches `s.tsv` with no such
condition. `embed: false` alone therefore does NOT hide a document from search.

`default_search: false` SHALL add an exclusion predicate to **both** arms of the candidate query,
suppressed when the caller names a `category` or `doc_type` filter. Validation SHALL reject
`default_search: true` with `embed: false` — nothing to rank without a vector.

A doc_type with `embed: false` SHALL NOT have embeddings generated or stored. Flipping `embed` true
later does not retroactively embed existing documents; they must be re-stored.

Documents excluded from default search remain fully readable through `get_document` and
`list_documents`.

#### Scenario: No embeddings written

- **WHEN** a document is stored whose doc_type has `embed: false`
- **THEN** its sections are stored with no embedding rows

#### Scenario: Embedded but hidden from default results

- **WHEN** a doc_type has `embed: true` and `default_search: false`, and an unfiltered query would otherwise match one of its documents
- **THEN** the document is absent from both the semantic and keyword arms

#### Scenario: Explicit filter restores semantic ranking

- **WHEN** the same query names that doc_type or its category
- **THEN** the document is returned with semantic ranking, because its embeddings exist

#### Scenario: Keyword arm honors the exclusion

- **WHEN** a doc_type has `embed: false` and `default_search: false`, and an unfiltered query lexically matches its text
- **THEN** the document is absent rather than surfacing through the keyword arm

#### Scenario: Contradictory rules rejected

- **WHEN** a policy write sets `default_search: true` and `embed: false`
- **THEN** the write is rejected with a validation error

### Requirement: Seeded defaults, with three intended changes

Seeding SHALL remain idempotent (`ON CONFLICT DO NOTHING`) so an admin's edited rows survive upgrades.
Values not listed are NULL and inherit from `reference`.

| doc_type | seeded values |
| --- | --- |
| `reference` | `staleness_days 90`; explicit defaults for every other column — guards, scan, lint, embed, default_search and prunable true, `write_mode replace`, `slug_format any`, `subcategory optional` |
| `project_state` | `staleness_days 14` |
| `audit` | `staleness_days 30` |
| `learning` | `staleness_days 180` |
| `preference` | `staleness_days 365` |
| `tool` | `staleness_days 90` |
| `journal` | `staleness_days 0`, `duplicate_guard false`, `cleanup_scan false`, `lint_stale_check false`, `default_search false`, `write_mode merge_sections`, `slug_format date`, `subcategory forbidden` |
| `handoff` | `staleness_days 0`, `duplicate_guard false`, `cleanup_scan false`, `lint_stale_check false`, `default_search false`, `prunable false`, `subcategory required`, `rules {"chain_previous": {"scope": "subcategory", "edge_type": "continues_from"}}` |

`journal` and `handoff` take `staleness_days: 0` because they are staleness-exempt today; their stored
values (10 and 3650) are never read and SHALL NOT be carried over as if they were live thresholds.

Everything reproduces current behavior **except three deliberate changes**:

1. `journal` and `handoff` get `default_search: false` — nothing excludes them from unfiltered results
   today, so an unfiltered query can return a journal entry ahead of the knowledge document that
   answers it. Both keep `embed: true`.
2. `journal` gets `write_mode: merge_sections` — today a store to an existing journal truncates the day.
3. `journal` gets `slug_format: date` — today any slug is accepted.

The `journal` row is the point of the exercise: the `eod` skill's convention — one document per day,
slug is the date, add a section rather than replacing — becomes data instead of prompt text any client
can get wrong.

#### Scenario: Everything else is unchanged

- **WHEN** an existing instance migrates and the seed runs
- **THEN** staleness results, duplicate-guard outcomes, lint findings, cleanup-scan pairs, embeddings, and handoff chaining are identical to before, for all 8 doc_types

#### Scenario: The three intended changes

- **WHEN** the migration completes
- **THEN** an unfiltered search no longer returns journals or handoffs while a filtered one still ranks them semantically; a store to an existing journal preserves its earlier sections; and a malformed journal slug is rejected

#### Scenario: Admin edits survive re-seeding

- **WHEN** an admin has changed a policy row and the server restarts, running the seed again
- **THEN** the edited row is left untouched

### Requirement: Write path order

Rules SHALL be applied in this order, and every violation SHALL be a validation error raised before any
write, inside the existing transaction, so no partial state is ever stored.

1. Authz — resolve tenant and write scope. Always first; no rule runs before authz.
2. Classify — `InferDocType(category, subcategory, slug)` on the client-supplied slug.
3. Load rules — the in-memory effective set for that doc_type.
4. Identity — validate `subcategory`, then `slug_format`.
5. Content — split into sections by `##`.
6. Resolve target — the existing document at `(tenant, category, subcategory, slug)`.
7. Write mode — decide which sections are deleted, upserted, added, or rejected.
8. Guards — duplicate guard, if `duplicate_guard` and not `force`.
9. Embed — generate and store embeddings only if `embed`.
10. Post-write links — `chain_previous`.
11. History and audit — unchanged.

#### Scenario: Classification precedes identity validation

- **WHEN** a document is stored
- **THEN** its doc_type is resolved from the client-supplied slug before `slug_format` is checked, and validation never alters the slug

#### Scenario: A rejected write leaves nothing behind

- **WHEN** a store fails `slug_format` validation
- **THEN** no document, section, embedding, or history row is created

### Requirement: Policy is instance-wide

Policy rows SHALL apply to every tenant. Per-tenant policy overrides are out of scope.

The existing tenant toggles (`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled` on `tenants`)
SHALL keep their meaning and precedence: they gate whether a mechanism runs for a tenant at all, while
the policy decides which doc_types it applies to once it does.

#### Scenario: One row serves every tenant

- **WHEN** the `learning` row's `staleness_days` changes
- **THEN** every tenant's learning documents use the new threshold

#### Scenario: Tenant toggle still gates the mechanism

- **WHEN** a tenant has `cleanup_scan_enabled = false` and a doc_type has `cleanup_scan` true
- **THEN** that tenant is not scanned, matching current behavior
