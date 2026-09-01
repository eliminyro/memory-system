## Why

How a document is maintained — whether its verification clock runs, whether the duplicate guard
fires, whether the cleanup scanner considers it, whether it is embedded and searchable, whether it can
be pruned — is decided by three hardcoded maps in `internal/models/staleness.go` plus two hardcoded
`doc_type == handoff` checks. Changing any of it means editing Go and cutting a release.

Some of a document type's behavior isn't even in the server. Journals are "one document per day, slug
is the date, add a section rather than replacing" — a convention that lives in the `eod` skill's
prompt. `StoreDocument` on an existing path deletes every section and rewrites
(`internal/service/memory.go:1192`), so a client that doesn't know the read-append-write dance
silently truncates the day. That is a data-loss footgun sitting behind prompt text.

`staleness_thresholds` already proves the shape this wants: a table keyed by doc_type, seeded
`ON CONFLICT DO NOTHING` so an operator's edits survive upgrades. It just expresses one thing — a day
count — when a doc_type carries a whole bundle of behavior, some of it currently unexpressible
anywhere.

Deriving the doc_type from the category stays exactly as it is. That derivation keeps categories
free-form while giving behavior somewhere to attach; it is not what needs changing.

## What Changes

- `staleness_thresholds` becomes `doc_type_policies`, one row per doc_type, holding ten rules:
  - **Nullable typed columns** — `staleness_days`, `duplicate_guard`, `cleanup_scan`,
    `lint_stale_check`, `embed`, `default_search`, `prunable`, `write_mode`, `slug_format`,
    `subcategory`. NULL means inherit from the `reference` row, which keeps NULL distinguishable from
    `staleness_days = 0` ("never check").
  - **One `rules` JSONB** for structured and experimental rules — `chain_previous` today, later
    `default_order` and a format escape hatch. Constrained only to "must be a JSON object", so
    experimenting with a key needs no migration.
- **`write_mode`** (`replace` | `merge_sections` | `append_only`) closes the truncation hole.
  `merge_sections` upserts by heading: an incoming section replaces the one with the same heading, a
  new heading is added, an existing heading absent from the payload is left alone. Idempotent, unlike
  plain append.
- **`slug_format`** validates identity on write. `journal/sept-1` is rejected at the door instead of
  becoming a junk document.
- **`subcategory`** (`optional` | `required` | `forbidden`) — handoffs need a project, journals must
  not have one.
- **`embed` and `default_search` are separate rules.** The semantic arm of the search query skips
  sections with no embedding, but the keyword arm matches `s.tsv` regardless, so hiding a doc_type from
  unfiltered results needs its own predicate on both arms. Separating them is what lets journals stay
  findable on request while dropping out of default results.
- `episodicDocTypes`, `neverPruneDocTypes`, and their four predicates are replaced by rule reads at
  their 6 call sites. The two hardcoded handoff checks read `chain_previous`.
- **The 5-minute TTL cache is removed.** Rules load and validate at boot — a bad row stops the app,
  naming the doc_type and field — and the admin surface recomputes in process on write. No polling, no
  restart, and fail-fast instead of silent degradation.
- **Rules are edited only through the admin surface**, gated to instance admin and audited to
  `override_log`. Raw SQL editing is unsupported: it bypasses validation and invalidation.

Seeded rows reproduce today's behavior for all 8 doc_types **except three deliberate changes**, called
out separately so the test oracle stays honest: `journal` and `handoff` leave default search;
`journal` gets `merge_sections`; `journal` gets `slug_format: date`.

Explicitly unchanged:

- **`InferDocType` and the category→doc_type derivation.** The Go switch stays, `projects` slug rules
  included. A category the switch doesn't recognize still lands on `reference` and its 90-day clock.
- **Policy keyed on doc_type, not category.** `projects` is one category holding three doc_types at
  14, 30 and 90 days.
- **Journal and handoff stay embedded.** They leave *unfiltered* results only; a query naming their
  category or doc_type still ranks them semantically, and `resume()` walks handoff chains untouched.
- **Cross-tenant read scope.** A trust boundary, not a maintenance preference, and not a rule.
- **Server-side slug derivation.** Deferred — no timezone field exists, so a derived date would be UTC
  and an evening journal entry would land on tomorrow.

## Capabilities

### New Capabilities

- `doc-type-policy`: the `doc_type_policies` table, its ten rules and their effect on the write path
  and the curation mechanisms, inheritance from `reference`, boot-time load and validation,
  admin-only editing with in-process recomputation, and seeding.

### Modified Capabilities

None. `openspec/specs/` holds no spec files, so the behaviors this refactors have nothing to delta
against. Their current behavior is captured as the seeded defaults in the spec, which is what the
tests assert.

## Impact

- `internal/models/staleness.go` — three maps and four predicates go away; the default rule set replaces `DefaultStalenessThresholds`. `InferDocType` untouched.
- `internal/models/` — new `DocTypePolicy` model with nullable typed fields plus `Rules`.
- `internal/database/database.go:249` — migration adding the columns and `rules`, backfilling `days` into `staleness_days`, dropping `days`, renaming the table; thin named CHECK constraints; seed loop writes full rows.
- `internal/staleness/staleness.go:68` — the TTL cache is replaced by a boot-loaded, admin-invalidated policy store.
- `internal/service/memory.go:1123` — duplicate guard reads `duplicate_guard`.
- `internal/service/memory.go:1181-1192` — section handling branches on `write_mode`.
- `internal/service/memory.go:1255`, `internal/repository/document.go:158` — chaining reads `chain_previous`.
- `internal/service/staleness_view.go` — two `IsEpisodic` calls become rule reads.
- `internal/repository/lint.go` — three `EpisodicDocTypes()` bindings become rule-derived arrays.
- `internal/repository/section.go` — both arms of the search candidate query gain the `default_search` predicate.
- Embedding write path — skipped when `embed` is false.
- `internal/mcp/admin_tools.go`, `internal/server/admin_api.go` — read and write policy rows.

Data: `staleness_thresholds` migrates in place to `doc_type_policies`. No document rows touched.

Callers: no tool signature changes. Behavior changes only for the three seeded exceptions above.
