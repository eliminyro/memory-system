## ADDED Requirements

### Requirement: Slug prefix filter

`list_documents` SHALL accept an optional `slug_prefix` and return only documents whose slug begins
with it. Because journal and similar date-identified documents carry the date in the slug
(`journal/YYYY-MM-DD`), a prefix is the primitive that answers "which day, month, or year is this
document about".

Any `%` or `_` in the caller's value SHALL be escaped before it reaches the LIKE pattern, so a prefix
cannot be turned into a wildcard.

#### Scenario: Month prefix

- **WHEN** `list_documents` is called with `category="journal"` and `slug_prefix="2026-08"`
- **THEN** only journal documents whose slug begins with `2026-08` are returned

#### Scenario: Year prefix

- **WHEN** the same call uses `slug_prefix="2026"`
- **THEN** every 2026 journal document is returned

#### Scenario: Wildcard characters are literal

- **WHEN** `slug_prefix` is `100%`
- **THEN** only slugs literally beginning with `100%` match, and the `%` does not act as a wildcard

#### Scenario: Omitted prefix changes nothing

- **WHEN** `slug_prefix` is omitted
- **THEN** the result set is identical to today's

### Requirement: Ordering

`list_documents` SHALL accept `order_by` with the values `slug`, `created_at`, `updated_at`, and
`title`, defaulting to `slug`; and `order` with the values `asc` and `desc`, defaulting to `asc`.

`order_by` values SHALL map to fixed column expressions through an allowlist. A caller-supplied string
SHALL NOT reach the query builder's ordering clause.

#### Scenario: Newest journals first

- **WHEN** `list_documents` is called with `category="journal"`, `order_by="slug"`, `order="desc"`, `limit=7`
- **THEN** the seven most recent journal entries are returned, newest first

#### Scenario: Default ordering is unchanged

- **WHEN** `order_by` and `order` are both omitted
- **THEN** documents are returned in the current order — `category`, `subcategory`, `slug`, `id`

#### Scenario: Unknown order field rejected

- **WHEN** `order_by` names a field outside the allowlist
- **THEN** the call is rejected with a validation error and no query runs

#### Scenario: Ordering clause is not caller-controlled

- **WHEN** `order_by` carries SQL fragments rather than an allowlisted value
- **THEN** it is rejected by the same validation, and nothing from the input is interpolated into the query

### Requirement: Ordering stays deterministic under pagination

Offset paging is total today because the sort key is unique across the aggregated tenant set — no page
skips or duplicates a row (design D6). A caller-chosen `order_by` breaks that on its own: `created_at`,
`updated_at`, and `title` are all non-unique.

Every ordering SHALL therefore append `id` as its final tiebreaker.

#### Scenario: Paging over a non-unique sort key

- **WHEN** documents are listed ordered by `updated_at` with several rows sharing the same timestamp, paged with `limit` and `offset`
- **THEN** every document appears exactly once across the pages, and none is skipped

#### Scenario: Repeated identical query is stable

- **WHEN** the same ordered, paged query runs twice with no intervening writes
- **THEN** both responses list the same documents in the same order

### Requirement: Pagination parameters on the listing surface

`list_documents` SHALL accept `limit` and `offset`, which the repository's `List` already supports.
Omitting `limit` SHALL keep the current unbounded behavior — `limit=0` — so no existing caller changes
behavior.

#### Scenario: Bounded listing

- **WHEN** `list_documents` is called with `limit=10`
- **THEN** at most 10 documents are returned

#### Scenario: Unbounded by default

- **WHEN** `limit` is omitted
- **THEN** every matching document is returned, as today

#### Scenario: Offset without limit

- **WHEN** `offset` is supplied and `limit` is omitted
- **THEN** the call is rejected, since an offset into an unbounded result set is meaningless

### Requirement: MCP and REST parity

The REST browse endpoint SHALL accept the same `slug_prefix`, `order_by`, `order`, `limit`, and
`offset` parameters, apply identical validation, and return the same documents in the same order for
the same inputs.

#### Scenario: Both surfaces agree

- **WHEN** the MCP tool and the REST endpoint are called with the same filters, ordering, and paging under the same credentials
- **THEN** both return the same documents in the same order

#### Scenario: REST rejects the same invalid input

- **WHEN** the REST endpoint receives an `order_by` outside the allowlist
- **THEN** it is rejected with a validation error, matching the MCP tool
