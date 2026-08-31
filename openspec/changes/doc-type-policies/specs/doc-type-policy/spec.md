## ADDED Requirements

### Requirement: Doc_type policy table

The system SHALL store one policy row per doc_type, replacing the compiled-in `episodicDocTypes` and
`neverPruneDocTypes` maps. Each row SHALL carry:

| Field | Meaning |
| --- | --- |
| `doc_type` | Primary key |
| `staleness_days` | Threshold in days; `0` means the staleness clock never runs for this doc_type |
| `duplicate_guard` | Whether the write-time duplicate guard evaluates this doc_type |
| `cleanup_scan` | Whether the near-duplicate scanner considers this doc_type |
| `lint_stale_check` | Whether `lint_memory` reports this doc_type as stale |
| `prunable` | Whether retention/eviction may remove documents of this doc_type |
| `search_default_visible` | Whether unfiltered `search_memory` returns this doc_type |
| `behavior` | JSONB parameters for behaviors that apply to some doc_types rather than all |

The columns SHALL be limited to switches every doc_type answers meaningfully. A behavior that applies
to a subset — handoff chaining being the only one today — belongs in `behavior`, not in a column that
is null or false for every other row.

The table SHALL be the widened `staleness_thresholds` table rather than a second config surface, so
the day threshold and the flags governing the same doc_type cannot drift apart.

The policy table SHALL be the registry of known doc_types. `ValidDocTypes` stops being the authority:
a doc_type exists because it has a policy row, not because it is a compiled-in constant, which is what
makes adding one a data change.

#### Scenario: Every seeded doc_type has a policy

- **WHEN** the server finishes migrating
- **THEN** a policy row exists for each of the 8 doc_types that were compiled in before this change

#### Scenario: A doc_type exists by virtue of its row

- **WHEN** a policy row is added for a doc_type that is not a compiled-in constant
- **THEN** it is a valid target for a mapping row and its policy applies, with no code change

#### Scenario: Unknown doc_type falls back

- **WHEN** a policy is requested for a doc_type with no row
- **THEN** the `reference` row's policy is returned, matching the current threshold fallback

### Requirement: Seeded defaults preserve current behavior

Seeding SHALL reproduce today's compiled-in behavior exactly. The seed SHALL remain idempotent
(`ON CONFLICT DO NOTHING`) so an operator's edited rows survive upgrades.

| doc_type | staleness_days | duplicate_guard | cleanup_scan | lint_stale_check | prunable | search_default_visible | behavior |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `project_state` | 14 | true | true | true | true | true | `{}` |
| `audit` | 30 | true | true | true | true | true | `{}` |
| `learning` | 180 | true | true | true | true | true | `{}` |
| `preference` | 365 | true | true | true | true | true | `{}` |
| `tool` | 90 | true | true | true | true | true | `{}` |
| `reference` | 90 | true | true | true | true | true | `{}` |
| `journal` | 0 | false | false | false | true | true | `{}` |
| `handoff` | 0 | false | false | false | false | true | `{"chain_previous": true}` |

`journal` and `handoff` carry `staleness_days = 0` because they are exempt from staleness evaluation
today; their current stored values (10 and 3650) are never read and SHALL NOT be carried over as if
they were live thresholds.

#### Scenario: Upgrade changes nothing observable

- **WHEN** an existing instance migrates and the seed runs
- **THEN** staleness results, duplicate-guard outcomes, lint findings, cleanup-scan pairs, search results, and handoff chaining are identical to before the migration for all 8 doc_types

#### Scenario: Operator edits survive re-seeding

- **WHEN** an operator has changed a policy row and the server restarts, running the seed again
- **THEN** the edited row is left untouched

### Requirement: Curation mechanisms read the policy

Each curation mechanism SHALL consult the policy rather than a compiled-in doc_type set:

- Staleness evaluation SHALL skip a section whose doc_type has `staleness_days = 0`, marking it
  neither stale nor guarded, and SHALL never withhold its content under hard staleness mode.
- The write-time duplicate guard SHALL be skipped when `duplicate_guard` is false.
- The near-duplicate scanner SHALL exclude doc_types whose `cleanup_scan` is false.
- `lint_memory`'s stale check SHALL exclude doc_types whose `lint_stale_check` is false.
- Retention and eviction SHALL never remove a document whose doc_type has `prunable` false.
- Unfiltered `search_memory` SHALL exclude doc_types whose `search_default_visible` is false; an
  explicit `category` or `doc_type` filter naming them SHALL return them.
- A new document whose doc_type carries `chain_previous` in `behavior` SHALL link to the prior latest
  document in its scope, as handoffs do today.

#### Scenario: Turning off a guard by configuration

- **WHEN** an operator sets `duplicate_guard` false for `learning` and a near-identical learning document is written
- **THEN** the write succeeds with no `similar_exists` status and no code change was required

#### Scenario: Hiding a doc_type from default search

- **WHEN** a doc_type's `search_default_visible` is false and an unfiltered search would otherwise match one of its documents
- **THEN** the document is absent, and a search filtered to that doc_type returns it

#### Scenario: Chaining is policy-driven

- **WHEN** `behavior` carries `chain_previous` for a doc_type other than `handoff` and a document of that type is created
- **THEN** it links to the prior latest document of that type in its scope

#### Scenario: Chaining removed by configuration

- **WHEN** `chain_previous` is removed from the `handoff` row's `behavior` and a handoff is created
- **THEN** it is stored with no link to the prior handoff, and no code change was required

#### Scenario: Zero-day staleness never guards

- **WHEN** a section's doc_type has `staleness_days = 0` and the section was verified years ago and mentions a code path
- **THEN** it is returned in full, unguarded, with no `needs_verification` status

### Requirement: Behavior parameters are registry-validated

The `behavior` object holds parameters for behaviors that apply to a subset of doc_types. The system
SHALL maintain a registry of the keys it implements, with the value shape each accepts, and SHALL
reject a write carrying a key not in that registry or a value of the wrong shape.

Silent acceptance is the failure this prevents: a misspelled key in a schemaless column would be
stored successfully, read by nothing, and produce no error at any point.

A key SHALL accept either a bare `true` or a parameter object where the behavior takes parameters, so
a behavior can gain parameters without a schema migration. `chain_previous` is the only registered key
this change introduces.

#### Scenario: Unknown key rejected

- **WHEN** a policy write sets `behavior` to `{"chain_previos": true}`
- **THEN** the write is rejected naming the unrecognized key, and the stored row is unchanged

#### Scenario: Wrong value shape rejected

- **WHEN** a policy write sets a registered key to a value the registry does not accept for it
- **THEN** the write is rejected with a validation error

#### Scenario: Empty behavior is valid

- **WHEN** a policy row's `behavior` is `{}`
- **THEN** the row is valid and no subset behavior applies to that doc_type

#### Scenario: Registry is discoverable

- **WHEN** an operator inspects the policy surface
- **THEN** the registered behavior keys and their accepted value shapes are returned, so a valid row can be written without reading the source

### Requirement: Policy is instance-wide

Policy rows SHALL be instance-wide: one row per doc_type, applying to every tenant. Per-tenant policy
overrides are out of scope.

The existing tenant-level toggles (`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled` on
`tenants`) SHALL keep their current meaning and precedence. They gate whether a mechanism runs for a
tenant at all; the policy decides which doc_types it applies to once it does. A tenant with
`cleanup_scan_enabled = false` SHALL therefore get no cleanup scan regardless of any doc_type's
`cleanup_scan` value.

#### Scenario: One row serves every tenant

- **WHEN** the `learning` row's `staleness_days` is changed
- **THEN** every tenant's learning documents use the new threshold

#### Scenario: Tenant toggle still gates the mechanism

- **WHEN** a tenant has `cleanup_scan_enabled = false` and a doc_type has `cleanup_scan = true`
- **THEN** that tenant is not scanned, matching current behavior

### Requirement: Policy rows are validated and inspectable

Because a wrong row silently weakens a guard with no compile-time check, the system SHALL validate
policy writes and SHALL make the effective policy readable.

Validation SHALL reject an unknown doc_type, a negative `staleness_days`, an unregistered `behavior`
key, and any row that would leave a doc_type both non-prunable and invisible to every read path. The
policy table SHALL be readable through the admin surface, and `lint_memory` SHALL report doc_types
whose policy disables every maintenance signal.

#### Scenario: Invalid row rejected

- **WHEN** a policy write names an unknown doc_type or a negative day count
- **THEN** the write is rejected with a validation error and no row changes

#### Scenario: Policy is readable

- **WHEN** an operator inspects the policy surface
- **THEN** every doc_type's resolved values are returned, including doc_types falling back to the `reference` row

#### Scenario: Lint flags a fully-silenced doc_type

- **WHEN** a doc_type's policy disables staleness, lint, cleanup scan, and duplicate guard
- **THEN** `lint_memory` reports it, so an unmaintained doc_type is visible rather than silent
