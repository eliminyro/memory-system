## Why

There is no way to retrieve documents by date. Verified across the read paths: the only date
predicates in the codebase are internal maintenance — import-job staleness, `mutation_history`
pruning, and lint's `updated_at < NOW() - interval`. Nothing user-facing filters or sorts by time.

That bites hardest on journals, where the date *is* the identity (`journal/YYYY-MM-DD`). Today the
options are `get_document` for one exact day, or `list_documents` for every journal entry ever. The
listing is at least chronological already, since `List` orders by slug and the slug is the date — but
it is unbounded and unfilterable, so "what did I write last week" means listing everything and reading
it by eye. One entry today; 365 a year.

This also unblocks a decision in `doc-type-policies`. Hiding `journal` from default search removes its
only flexible retrieval path, leaving exact-slug or list-everything. Landing this first means that
change can hide journals without taking away the ability to find them.

## What Changes

- `list_documents` gains `slug_prefix`: matches `slug LIKE '<prefix>%'`, so `2026-08` returns August
  and `2026` returns the year. `%` and `_` in the input are escaped, so a caller's prefix cannot become
  a wildcard.
- `list_documents` gains `order_by` (`slug` | `created_at` | `updated_at` | `title`, default `slug`)
  and `order` (`asc` | `desc`, default `asc`).
- `list_documents` exposes `limit` and `offset`. These already exist on the repository's `List`; the
  MCP tool deliberately passes `limit=0` to stay unbounded (`internal/mcp/tools.go:563`, design D2).
  That default does not change — omitting `limit` still returns everything.
- Every ordering appends `id` as a final tiebreaker, preserving the property the current composite sort
  provides: a unique sort key, so offset paging never skips or duplicates a row (design D6).
- `order_by` values map to fixed column expressions through an allowlist. No caller string reaches
  GORM's `.Order()`.
- The REST browse endpoint gains the same parameters, so both surfaces agree.

Out of scope:

- Timestamp range filters (`created_after` / `created_before`). A different question — "what did I
  write recently" for any doc type, rather than "which day is this document about" — and nothing needs
  it yet. Noted in the design so the distinction survives.
- Any change to `search_memory`. This is browse and retrieval, not ranking.

## Capabilities

### New Capabilities

- `document-listing`: filtering, ordering, and pagination parameters on the document listing surface,
  including the deterministic-ordering and injection-safety constraints they must satisfy.

### Modified Capabilities

None. `openspec/specs/` holds no spec files, so the current listing behavior has nothing to delta
against. It is captured as the default-parameter scenarios in the spec.

## Impact

- `internal/repository/document.go:133` — `List` gains slug-prefix, order-by, and direction parameters; the ordering clause becomes allowlist-driven with an `id` tiebreaker.
- `internal/mcp/tools.go:226` — `ListDocumentsInput` gains `slug_prefix`, `order_by`, `order`, `limit`, `offset`.
- `internal/mcp/tools.go:558` — `ListDocuments` passes them through, keeping `limit=0` as the default.
- `internal/server/api.go` — the REST browse handler accepts and validates the same parameters.

Data: none. No schema change, no migration.

Callers: additive only. Every existing call keeps its current behavior and result order.
