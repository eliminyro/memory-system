## Context

Verified across the read paths on this branch: nothing user-facing filters or sorts documents by time.
The only date predicates in the codebase are internal maintenance — `internal/repository/import_job.go:142`
(stale running jobs), `internal/repository/mutation_history.go:71` (history pruning), and
`internal/repository/lint.go:124` (`updated_at < NOW() - make_interval`).

What exists on the listing path:

- `DocumentRepository.List` (`internal/repository/document.go:133`) takes `category`, `subcategory`,
  `limit`, `offset`. It orders by `category, subcategory, slug, id` and applies `limit`/`offset` only
  when `limit > 0`.
- The comment at `document.go:131` states why that composite ordering matters: the sort key is unique
  across the aggregated tenant set, so offset paging is total — no page skips or duplicates a row
  (design D6).
- `ListDocumentsInput` (`internal/mcp/tools.go:226`) exposes only `category`, `subcategory`,
  `tenant_id`. `ListDocuments` (`tools.go:558`) passes `limit=0` deliberately: "limit=0 keeps the MCP
  tool unbounded (design D2) — only the HTTP browse path paginates."
- `list_documents` returns `{id, path, title}` per document, so listing is already cheap to scan.

Because slugs for date-identified documents are `YYYY-MM-DD` and `List` already sorts by slug, the
current listing is chronological. It is unbounded and unfilterable, which is the whole gap.

## Goals / Non-Goals

**Goals:**

- Retrieve documents by date where the date lives in the slug, without listing everything.
- Bound and order a listing without breaking offset paging.
- No behavior change for any existing caller.

**Non-Goals:**

- Timestamp range filters. See D3.
- Anything touching `search_memory`. This is browse, not ranking.
- Schema changes. No migration, no new column, no new index.

## Decisions

**D1 — Filter on the slug, not on `created_at`.**

For a journal, the slug is the day the entry is *about*; `created_at` is when the row was written.
They coincide most of the time and diverge exactly when it matters — a backfilled entry written on the
5th about the 1st. `slug_prefix` answers the question being asked.

It also composes with what is already there: `category="journal"` plus a slug prefix reuses the
existing sort, so the result is chronological with no extra work.

*Alternative considered:* a typed `date` column on `documents`, populated for date-identified types.
Correct for a system where dates are first-class, but it needs a migration, a backfill, a rule for
which types get one, and it duplicates information the slug already carries.

**D2 — `order_by` is an allowlist mapped to fixed expressions; every ordering appends `id`.**

Two separate reasons, both load-bearing.

Injection: GORM's `.Order()` takes a string. A caller-supplied value reaching it is an injection
vector, so `order_by` is an enum and the mapping to a column expression lives in Go.

Determinism: `created_at`, `updated_at`, and `title` are all non-unique. Offset paging over a
non-unique sort key skips and duplicates rows — the exact property `document.go:131` says the current
composite ordering exists to guarantee. Appending `id` keeps the sort key unique for every choice of
`order_by`, so D6 continues to hold rather than holding only for the default.

*Alternative considered:* keyset pagination (`WHERE (sort_key, id) > (:last_key, :last_id)`), which is
correct regardless of uniqueness and does not degrade at high offsets. Right answer at scale, and the
project already uses a keyset cursor for the issuesync poll elsewhere. Rejected here because `List` is
offset-based today and every caller of it would change; the `id` tiebreaker fixes the correctness
problem without touching the pagination model. Revisit if listings grow past the point where offset
scanning hurts.

**D3 — No timestamp range filters in this change.**

`created_after` / `created_before` on `created_at` answers a different question: "what did I write
recently", for any doc type, regardless of what the document is about. Nothing needs it yet, and
adding it means deciding whether it filters on `created_at` or `updated_at`, and how it interacts with
`slug_prefix` when both are supplied. Additive later.

**D4 — `limit` stays unbounded by default.**

Exposing `limit` on the MCP tool does not change its default. `ListDocuments` keeps passing `limit=0`
when the caller omits it, so design D2's choice — the MCP tool is unbounded, the HTTP browse path
paginates — survives. `offset` without `limit` is rejected rather than silently ignored, because an
offset into an unbounded result set means nothing.

## Risks / Trade-offs

- **`slug LIKE 'prefix%'` may not use the existing btree.** `idx_doc_tenant_path_active` covers
  `(tenant_id, category, COALESCE(subcategory,''), slug) WHERE archived_at IS NULL`, but a prefix LIKE
  only uses a btree when the index carries `text_pattern_ops` or the database collation is C. →
  Irrelevant at journal scale (hundreds of rows, and `category` already narrows the scan). Worth
  recording so a future seq scan is not a mystery; the fix is a `text_pattern_ops` index, added when
  something is actually slow.
- **A caller can now ask for an ordering the index does not serve**, e.g. `title desc` across every
  category. → Bounded by `limit` in practice, and the table is small. Not worth pre-optimizing.
- **Escaping `%` and `_` is easy to forget in the second implementation** (REST after MCP). → Both
  surfaces call one shared validation-and-build helper rather than assembling the clause twice; the
  parity scenarios in the spec assert it.
- **Adding parameters to a listing tool invites callers to page when they should filter.** → The tool
  description should say plainly that `slug_prefix` is the cheap path and paging is for large
  categories.

## Migration Plan

No migration. Every parameter is optional, every default reproduces current behavior, and the change
can ship in any release.

Sequencing: land this before `doc-type-policies`, which hides `journal` from default search. Doing it
in that order means journals lose the meaning path only once the date path exists.

## Open Questions

- Should `order_by` accept a second field (`order_by: ["slug", "title"]`) rather than one? Nothing needs
  it, and one field plus the `id` tiebreaker covers every case raised so far.
- Does the REST browse endpoint need a total-count header for paging UIs? Out of scope here; it would
  cost a second query.
