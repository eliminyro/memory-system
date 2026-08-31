## ADDED Requirements

### Requirement: Category to doc_type mapping table

The system SHALL store the category→doc_type mapping as data in a `category_doc_types(category,
doc_type)` table, seeded with the categories `InferDocType` recognizes today. Adding a category with
its own maintenance behavior SHALL require no code change: a mapping row plus a policy row for the
doc_type it names.

The mapping SHALL be instance-wide — one taxonomy for every tenant. (See the design's open question:
adding a `tenant_id` column later is additive.)

| category | doc_type |
| --- | --- |
| `projects` | `reference` (classification rules refine it — see the next requirement) |
| `learnings` | `learning` |
| `preferences` | `preference` |
| `tools` | `tool` |
| `journal` | `journal` |
| `handoffs` | `handoff` |

#### Scenario: Seeded mapping reproduces current classification

- **WHEN** a document is stored in each of the six seeded categories
- **THEN** each is assigned the same doc_type it would have received from `InferDocType` before this change

#### Scenario: A new category is a data change

- **WHEN** an operator adds a `runbooks → runbook` mapping row and a `runbook` policy row, then stores a document in category `runbooks`
- **THEN** the document gets doc_type `runbook` and is maintained per that policy row, with no code change

#### Scenario: Unmapped category falls back

- **WHEN** a document is stored in a category with no mapping row
- **THEN** it is assigned doc_type `reference`, matching current behavior for unrecognized categories

#### Scenario: Mapping validated against the policy table

- **WHEN** a mapping row names a doc_type with no policy row
- **THEN** the write is rejected, so a mapping cannot point at a doc_type whose behavior is undefined

### Requirement: Classification within a category stays in code

Rules that pick a doc_type from something other than the category name SHALL remain in `InferDocType`
and SHALL take precedence over the category's mapping row. These are the `projects` slug rules:

- slug `state` → `project_state`
- slug containing `audit`, `plan`, `design`, or `backlog` (case-insensitive) → `audit`

Expressing these as data would require a pattern language; the mapping table delivers extensibility
without one. A mapping row therefore sets a category's *default* doc_type, which in-code rules may
refine.

#### Scenario: Slug rule beats the mapping row

- **WHEN** a document is stored at `projects/hilo/state`
- **THEN** its doc_type is `project_state`, not the `projects` mapping row's `reference`

#### Scenario: Marker rule beats the mapping row

- **WHEN** a document is stored at `projects/hilo/backend-audit-campaign`
- **THEN** its doc_type is `audit`

#### Scenario: Mapping row applies when no rule matches

- **WHEN** a document is stored at `projects/hilo/backend`
- **THEN** its doc_type is the `projects` mapping row's value, `reference`

### Requirement: Unmapped categories are reported

Because an unmapped category silently inherits `reference` and its 90-day clock, `lint_memory` SHALL
report categories that appear in documents but have no mapping row.

#### Scenario: Lint surfaces an unmapped category

- **WHEN** documents exist in a category with no mapping row
- **THEN** `lint_memory` reports the category and the number of documents in it

#### Scenario: No finding when every category is mapped

- **WHEN** every category present in documents has a mapping row
- **THEN** `lint_memory` reports no unmapped-category finding
