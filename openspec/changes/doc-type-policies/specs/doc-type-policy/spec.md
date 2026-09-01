## ADDED Requirements

### Requirement: Doc_type policy table

The system SHALL store one policy row per doc_type, replacing the compiled-in `episodicDocTypes` and
`neverPruneDocTypes` maps:

```
doc_type_policies(doc_type TEXT PRIMARY KEY, rules JSONB NOT NULL DEFAULT '{}')
```

The row SHALL carry the type's whole behavior as a rule set rather than a fixed set of typed columns.
A policy row SHALL exist for every member of `ValidDocTypes`, which remains the authority on what
doc_types exist — `InferDocType` is still the only thing that mints one, so a genuinely new doc_type
is a code change and its policy row accompanies it.

The table SHALL be the migrated `staleness_thresholds` table rather than a second config surface, so
a doc_type's day threshold and the rest of its behavior cannot drift apart.

#### Scenario: Every valid doc_type has a policy

- **WHEN** the server finishes migrating
- **THEN** a policy row exists for every member of `ValidDocTypes`

#### Scenario: Unknown doc_type falls back

- **WHEN** a policy is requested for a doc_type with no row
- **THEN** the `reference` row's rule set is returned, matching the current threshold fallback

### Requirement: Registered rule keys

The system SHALL maintain a registry of the rule keys it implements and the value shape each accepts.
A key SHALL accept either a bare value or a parameter object where the rule takes parameters, so a
rule can gain parameters without a schema migration.

| Key | Shape | Meaning |
| --- | --- | --- |
| `staleness_days` | int ≥ 0 | Verification threshold in days. `0` or absent means the clock never runs for this doc_type |
| `duplicate_guard` | bool | Whether the write-time duplicate guard evaluates this doc_type |
| `cleanup_scan` | bool | Whether the near-duplicate scanner considers this doc_type |
| `lint_stale_check` | bool | Whether `lint_memory` reports this doc_type as stale |
| `embed` | bool | Whether sections of this doc_type get embeddings generated on write |
| `default_search` | bool | Whether this doc_type appears in `search_memory` results when the caller names no `category` or `doc_type` filter |
| `prunable` | bool | Whether retention or eviction may ever remove documents of this doc_type |
| `chain_previous` | bool or object | Whether a new document links to the prior latest one in its scope |

#### Scenario: Empty rule set is valid

- **WHEN** a policy row's `rules` is `{}`
- **THEN** the row is valid and every rule falls back to the `reference` row's value

#### Scenario: Registry is discoverable

- **WHEN** an operator inspects the policy surface
- **THEN** the registered keys and their accepted value shapes are returned, so a valid row can be written without reading the source

### Requirement: Rule writes are validated

Because a wrong rule silently weakens a guard with no compile-time check, and a misspelled key in a
schemaless column would be stored successfully, read by nothing, and produce no error at any point,
the system SHALL reject on write:

- an unknown doc_type
- an unregistered rule key
- a value whose shape the registry does not accept for that key
- a negative `staleness_days`
- a rule set that would leave a doc_type both non-prunable and invisible to every read path

The policy table SHALL be readable through the admin surface, and `lint_memory` SHALL report
doc_types whose rule set disables every maintenance signal.

#### Scenario: Unregistered key rejected

- **WHEN** a policy write sets `rules` to `{"chain_previos": true}`
- **THEN** the write is rejected naming the unrecognized key, and the stored row is unchanged

#### Scenario: Wrong value shape rejected

- **WHEN** a policy write sets a registered key to a value the registry does not accept for it
- **THEN** the write is rejected with a validation error

#### Scenario: Invalid threshold rejected

- **WHEN** a policy write names an unknown doc_type or a negative `staleness_days`
- **THEN** the write is rejected with a validation error and no row changes

#### Scenario: Lint flags a fully-silenced doc_type

- **WHEN** a doc_type's rule set disables staleness, lint, cleanup scan, and duplicate guard
- **THEN** `lint_memory` reports it, so an unmaintained doc_type is visible rather than silent

### Requirement: Seeded defaults reproduce current behavior, with one intended change

Seeding SHALL reproduce today's compiled-in behavior, and SHALL remain idempotent
(`ON CONFLICT DO NOTHING`) so an operator's edited rows survive upgrades.

| doc_type | staleness_days | duplicate_guard | cleanup_scan | lint_stale_check | embed | default_search | prunable | chain_previous |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `project_state` | 14 | true | true | true | true | true | true | — |
| `audit` | 30 | true | true | true | true | true | true | — |
| `learning` | 180 | true | true | true | true | true | true | — |
| `preference` | 365 | true | true | true | true | true | true | — |
| `tool` | 90 | true | true | true | true | true | true | — |
| `reference` | 90 | true | true | true | true | true | true | — |
| `journal` | 0 | false | false | false | true | **false** | true | — |
| `handoff` | 0 | false | false | false | true | **false** | false | true |

`journal` and `handoff` carry `staleness_days: 0` because they are exempt from staleness evaluation
today; their currently stored values (10 and 3650) are never read and SHALL NOT be carried over as if
they were live thresholds.

Both keep `embed: true` — they are still found by meaning when a caller asks for them, and `resume()`
walks handoff chains. Their `default_search: false` is the **one deliberate behavior change** in this
change: today nothing excludes them from unfiltered results, so an unfiltered query can return a
journal entry ahead of the knowledge document that answers it. Every other seeded value reproduces
current behavior exactly.

#### Scenario: Upgrade changes nothing observable except default search for two types

- **WHEN** an existing instance migrates and the seed runs
- **THEN** staleness results, duplicate-guard outcomes, lint findings, cleanup-scan pairs, embeddings, and handoff chaining are identical to before the migration for all 8 doc_types

#### Scenario: The one intended change

- **WHEN** an unfiltered `search_memory` query runs after the migration and a journal or handoff document would previously have matched
- **THEN** it is absent, and the same query filtered to `category="journal"` or `doc_type="handoff"` still returns it with semantic ranking intact

#### Scenario: Operator edits survive re-seeding

- **WHEN** an operator has changed a policy row and the server restarts, running the seed again
- **THEN** the edited row is left untouched

### Requirement: Curation mechanisms read the rules

Each curation mechanism SHALL consult the doc_type's rule set rather than a compiled-in doc_type set:

- Staleness evaluation SHALL skip a section whose doc_type has `staleness_days` of `0` or absent,
  marking it neither stale nor guarded, and SHALL never withhold its content under hard staleness mode.
- The write-time duplicate guard SHALL be skipped when `duplicate_guard` is false.
- The near-duplicate scanner SHALL exclude doc_types whose `cleanup_scan` is false.
- `lint_memory`'s stale check SHALL exclude doc_types whose `lint_stale_check` is false.
- Retention and eviction SHALL never remove a document whose doc_type has `prunable` false.
- A new document whose doc_type carries `chain_previous` SHALL link to the prior latest document in
  its scope, as handoffs do today.

#### Scenario: Turning off a guard by configuration

- **WHEN** an operator sets `duplicate_guard` false for `learning` and a near-identical learning document is written
- **THEN** the write succeeds with no `similar_exists` status and no code change was required

#### Scenario: Chaining is rule-driven

- **WHEN** `chain_previous` is set for a doc_type other than `handoff` and a document of that type is created
- **THEN** it links to the prior latest document of that type in its scope

#### Scenario: Chaining removed by configuration

- **WHEN** `chain_previous` is removed from the `handoff` row and a handoff is created
- **THEN** it is stored with no link to the prior handoff, and no code change was required

#### Scenario: Zero-day staleness never guards

- **WHEN** a section's doc_type has `staleness_days: 0` and the section was verified years ago and mentions a code path
- **THEN** it is returned in full, unguarded, with no `needs_verification` status

### Requirement: Embedding and default search visibility are separate rules

`embed` and `default_search` SHALL be distinct rules, because skipping embeddings and hiding from
default results are different needs and the search query treats them differently:

- The semantic arm of the candidate query already requires `s.embedding IS NOT NULL`, so a doc_type
  with `embed: false` contributes nothing there.
- The keyword arm matches on `s.tsv` and does **not** require an embedding, so `embed: false` alone
  does NOT hide a document from search.

Therefore `default_search: false` SHALL add an exclusion predicate to **both** arms of the candidate
query, suppressed when the caller names a `category` or `doc_type` filter.

Validation SHALL reject `default_search: true` together with `embed: false` — a doc_type cannot be
ranked in default results with no vector to rank.

A doc_type with `embed: false` SHALL NOT have embeddings generated or stored on write. Flipping `embed`
to true later does not retroactively embed existing documents; they must be re-stored or re-embedded.

Documents excluded from default search remain fully readable: `get_document` and `list_documents`
return them unchanged.

#### Scenario: No embeddings written

- **WHEN** a document is stored whose doc_type has `embed: false`
- **THEN** its sections are stored with no embedding rows

#### Scenario: Embedded but hidden from default results

- **WHEN** a doc_type has `embed: true` and `default_search: false`, and an unfiltered query would otherwise match one of its documents
- **THEN** the document is absent from both the semantic and keyword arms of that query

#### Scenario: Explicit filter restores semantic ranking

- **WHEN** the same query names that doc_type or its category
- **THEN** the document is returned with semantic ranking, because its embeddings exist

#### Scenario: Keyword arm honors the exclusion

- **WHEN** a doc_type has `embed: false` and `default_search: false`, and an unfiltered query lexically matches its text
- **THEN** the document is absent, rather than surfacing through the keyword arm

#### Scenario: Contradictory rule set rejected

- **WHEN** a policy write sets `default_search: true` and `embed: false` for the same doc_type
- **THEN** the write is rejected with a validation error

#### Scenario: Still directly readable

- **WHEN** `get_document` or `list_documents` targets a document excluded from default search
- **THEN** it is returned normally

### Requirement: Policy is instance-wide

Policy rows SHALL be instance-wide: one row per doc_type, applying to every tenant. Per-tenant policy
overrides are out of scope.

The existing tenant-level toggles (`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled` on
`tenants`) SHALL keep their current meaning and precedence. They gate whether a mechanism runs for a
tenant at all; the policy decides which doc_types it applies to once it does.

#### Scenario: One row serves every tenant

- **WHEN** the `learning` row's `staleness_days` is changed
- **THEN** every tenant's learning documents use the new threshold

#### Scenario: Tenant toggle still gates the mechanism

- **WHEN** a tenant has `cleanup_scan_enabled = false` and a doc_type has `cleanup_scan` true
- **THEN** that tenant is not scanned, matching current behavior
