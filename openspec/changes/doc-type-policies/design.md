## Context

Doc_type behavior is spread across three compiled-in maps, two literal comparisons, and — for journals
— a skill's prompt:

- `episodicDocTypes` (`journal`, `handoff`) — exempt from duplicate guard, staleness, lint stale check,
  cleanup scan. `neverPruneDocTypes` (`handoff`) — permanent. `DefaultStalenessThresholds` — the seed
  for the one thing that is already a table.
- `internal/repository/document.go:158` and `internal/service/memory.go:1255` — `doc_type == handoff`
  drives chaining.
- `internal/service/memory.go:1192` — a store to an existing path deletes every section and rewrites.
  The journal convention that works around this (read, append, write back) lives in the `eod` skill.

Call sites reading the predicates: `internal/repository/lint.go` (3 `EpisodicDocTypes()` bindings),
`internal/service/memory.go:1123` (duplicate guard), `internal/service/staleness_view.go` (2
`IsEpisodic` calls). Six, plus the two chaining comparisons.

Other verified facts that shape the decisions:

- `InferDocType` is a category switch and the only thing that mints a doc_type. `store_memory` does not
  accept one. `ValidDocTypes` is referenced nowhere but its own definition and a test.
  `Document.Category` has no allowlist or CHECK, so categories are open while doc_types are closed.
- `staleness.ThresholdStore` (`internal/staleness/staleness.go:68`) caches thresholds behind a
  **5-minute TTL** with double-checked locking. Nothing in SQL reads the table: values reach search as a
  Go `map[string]int` (`section.go:80`) applied in `fuseHybrid` (`section.go:451`), and `lint.go` binds
  a Go-computed array.
- The search candidate query has optional `category` / `subcategory` / `doc_type` filters and no
  exclusions, so journals and handoffs are in unfiltered results today. Its semantic arm requires
  `s.embedding IS NOT NULL`; its keyword arm matches `s.tsv` with no such condition.
- No timezone or locale field exists in `internal/models` or `internal/config`.

## Goals / Non-Goals

**Goals:**

- A doc_type's behavior is data an admin can change without a release.
- Behavior that currently lives in a client's prompt — the journal convention — moves into the server.
- The 8 existing doc_types keep behaving as they do, except where a change is deliberate and named.
- A misconfigured rule is loud, not silent.

**Non-Goals:**

- Category→doc_type inference. `InferDocType` stays in Go (D2).
- Server-side slug derivation. Deferred (D6).
- Cross-tenant read scope, and per-tenant policy values.
- Per-document overrides. `Pinned` already covers the one per-document case.

## Decisions

**D1 — Hybrid storage: nullable typed columns for scalars, one loose JSONB for the rest.**

Scalar rules are typed columns; NULL means inherit from `reference`. The column type does the
enforcement, and NULL stays distinguishable from `staleness_days = 0`, which means "never check" and
would otherwise collide with an absent value. `rules JSONB` holds what isn't scalar — `chain_previous`
today — and is constrained only to "must be an object".

**This reverses an earlier decision in this document, twice over.** The previous version argued for
all-JSONB on the grounds that nothing in SQL reads the table, so no consumer needs a typed column. That
part is still true. What changed is the decision to enforce shape in the database: a key allowlist in a
`CHECK` makes every new rule key an `ALTER TABLE ... DROP/ADD CONSTRAINT`, which costs the same as
`ADD COLUMN` — so JSONB's headline advantage, "a new key needs no migration", only survives where the
JSONB is loosely constrained. Splitting the two keeps both properties: DB-enforced types on the rules
that have settled, migration-free experimentation on the ones that haven't. Graduating a rule from the
JSONB half to a column is one migration, shipped with the Go that reads it.

*Alternative considered:* all typed columns. Honest schema, but `chain_previous: {scope, edge_type}`
needs a JSONB column anyway, and every experiment costs a migration.

*Alternative considered:* an EAV table, `(doc_type, key, value)` with a foreign key into a registry
table so the database rejects unregistered keys. Attractive until you notice that cross-key rules
(`default_search` requires `embed`) are row-level `CHECK`s — with rules spread across rows they would
need a trigger instead.

**D2 — Classification stays entirely in Go; only behavior becomes data.**

`InferDocType` is untouched: the category switch and the `projects` slug rules. Deriving the doc_type
from a free-form category is what lets categories stay unconstrained while behavior still has somewhere
to attach, and it changes rarely enough that a Go line is the right cost.

Accepted consequence: a category the switch doesn't recognize lands on `reference` and its 90-day
clock, and a genuinely new doc_type means editing the switch.

*Alternative considered, and briefly specced:* a `category_doc_types` mapping table. Rejected as
complexity that breeds rigidity, for a derivation that changes once in a long while.

*Alternative considered:* let the writer declare the doc_type on `store_memory`. Rejected — derivation
from the category is the intended design, not a workaround for it.

**D3 — Boot-time load and validation, admin-write invalidation, no timer.**

The TTL is removed rather than widened. It pays a periodic query for a staleness window nothing
benefits from: rules don't change inside a session, and far more than five minutes passes between
sessions.

Loading at boot restores fail-fast — a bad row stops the server naming the doc_type and field, instead
of degrading silently at minute five of production. Recomputing on admin write keeps edits live. The two
were previously thought to be in tension; they only are if the trigger is a timer rather than an event.

*Known limit:* a second replica's admin write will not invalidate this replica's in-memory set.
`LISTEN`/`NOTIFY` on the table is the fix, deferred until multi-replica is real —
`internal/service/import_worker.go`'s peer-replica logic shows it is contemplated, so this is a real
deferral rather than a non-issue.

**D4 — Admin-only writes, which is why the CHECKs can stay thin.**

Rules are instance-wide, so writing one requires instance admin rather than the tenant manager level
that gates per-tenant toggles, and every write is audited to `override_log`. Raw SQL editing is
unsupported: it bypasses validation and invalidation both.

That makes Go-side validation the primary guard and `CHECK` constraints defense-in-depth — ranges,
enums, and `default_search`-requires-`embed`. Several narrowly named constraints, not one expression: a
violation reports only the constraint name, so the name has to be the diagnosis.

**D5 — `write_mode`, and why it is the most valuable rule here.**

A store to an existing path deletes every section (`memory.go:1192`). Writing one new journal section
therefore destroys the day's earlier entries unless the caller reads the document and resends it whole —
a dance that exists only in the `eod` skill's prompt. Any other client silently truncates.

`merge_sections` is specified as an upsert by heading rather than an append, so re-sending identical
content is a no-op. Plain append would duplicate.

`duplicate_guard: true` with a non-`replace` write mode is rejected rather than resolved: whether the
guard should compare the merged centroid or only the incoming sections are both defensible, and picking
one silently would be worse than refusing the combination. Journals already have the guard off.

**D6 — Identity validation never alters the slug, and derivation is deferred.**

`InferDocType` consumes the slug to classify before rules load, so changing the slug afterwards would
contradict the classification already made. Validation therefore rejects rather than corrects.

Derivation is deferred for a second reason: no timezone field exists anywhere in the models or config,
so a server-derived `{{date}}` would be UTC and an evening journal entry would land on tomorrow's
document. Validation gets most of the benefit — a malformed slug is caught at the door — with none of
that risk.

**D7 — `embed` and `default_search` are separate rules.**

The semantic arm requires `s.embedding IS NOT NULL`; the keyword arm does not. So `embed: false` alone
does not hide a document from search, and `default_search` needs its own predicate on both arms. Keeping
them separate is what lets journals stay embedded and semantically findable on request while dropping
out of unfiltered results.

**D8 — Predicates are deleted, not kept as wrappers.**

`IsEpisodic`, `IsPrunableEpisodic`, `EpisodicDocTypes`, `PrunableEpisodicDocTypes` go away rather than
becoming rule-backed shims. Keeping them would preserve "episodic" as a concept the schema no longer
has, and the term already does double duty for "append-per-day" and "exempt from curation". Where SQL
needs a doc_type array (`doc_type <> ALL(?)` in `lint.go`), it is computed in Go from the cached rules.

## Risks / Trade-offs

- **Three seeded values deliberately change behavior, inside a change otherwise sold as
  behavior-preserving.** → Each is named in the proposal and has its own spec scenario, so the test
  oracle asserts reproduction plus three intended differences rather than blanket equivalence.
- **`merge_sections` changes what `store_memory` means for journals.** A client that still does
  read-append-write will resend the whole document — which is harmless, because merge upserts by heading
  and is idempotent. That property is the reason for choosing merge over append.
- **The loose JSONB half accepts typos silently.** An experimental key nothing implements does nothing
  and errors nowhere. → `lint_memory` reports `rules` keys the server does not implement, so a typo is a
  finding rather than a mystery. Rejecting them would reintroduce the migration cost D1 exists to avoid.
- **Removing the TTL means an admin edit on one replica does not reach another.** → Named in D3, with
  `LISTEN`/`NOTIFY` as the known fix. Single-replica today.
- **`embed: false` is not retroactive.** Flipping it true later leaves existing documents unembedded. →
  Stated in the spec; a re-store is required.
- **Deleting the predicates touches 8 call sites at once.** → All eight are in the four files named in
  the proposal, and each has an existing test.
- **Boot-time validation can refuse to start a previously-working server** if new validation is stricter
  than a hand-edited row. → The migration validates every row and fails loudly during deployment rather
  than at first request.

## Migration Plan

1. `document-listing` ships first. It adds `slug_prefix` / `order_by` / `order` / `limit` to
   `list_documents`, which is what makes `journal`'s `default_search: false` safe — without it, hiding
   journals from unfiltered search leaves only exact-slug fetch or list-everything.
2. One migration: add the typed columns and `rules` to `staleness_thresholds`, backfill `days` into
   `staleness_days` (and `0` for `journal` / `handoff`), drop `days`, add the named `CHECK`s, rename the
   table to `doc_type_policies`.
3. Extend the seed loop to write full rows, still `ON CONFLICT DO NOTHING`.
4. Replace the call sites, add the write-path rules, delete the maps and predicates.
5. Ship as its own release — the rename and the dropped column are breaking — before `prompts-category`.

Rollback: the rename and dropped `days` are the breaking steps. Rolling back means renaming the table
and restoring `days` from `staleness_days`, so snapshot before migrating.

## Open Questions

- **Per-tenant verification ages.** They do not exist today: `staleness_thresholds` is keyed on doc_type
  alone, so ages are per-type and instance-wide (`learnings` 180d, `projects` 14/30/90d, `preferences`
  365d, `tools` 90d). Proceeding with that spread unchanged; per-tenant ages would add a tenant
  dimension to the key and would have to layer over the spread rather than replace it.
- **Held-back rules, each with its blocker.** `auto_expire_days` and `expires` need a nullable
  `documents.expires_at` column, filtered on read, which is not specced anywhere yet. `default_order`
  needs `document-listing`. `title_source`, `subcategory_format`, and a `{regex: ...}` format escape
  hatch have no justifying case so far.
- **Does `prunable` earn a column** when nothing implements retention or eviction? The cleanup scanner
  only enqueues near-duplicate pairs and prunes `mutation_history`; `ArchiveByID` has one caller,
  `link_documents ... supersedes`. Seeding it documents intent, but it is a rule nothing reads.
- **`LISTEN`/`NOTIFY` for multi-replica invalidation** — deferred, see D3.
