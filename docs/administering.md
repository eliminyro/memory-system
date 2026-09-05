# Administering your instance

This guide covers day-to-day operation of a running memory instance: creating
tenants, granting people access, and issuing/rotating/revoking API keys.

There are two administrative surfaces:

- **The web console (`/ui`)** — an admin signs in over OAuth and browses and
  edits memories, manages tenants, members, and per-document ACL grants, issues
  and rotates and revokes API keys, and runs archive imports. Every action is
  gated by that admin's authorization.
- **The MCP admin tools** — the same tenant, key, and user operations exposed to
  an already-privileged MCP client (for example, an agent whose key belongs to a
  global admin). Use these to script administration from a client you already
  have wired up.

The very first admin is provisioned over HTTP at `/bootstrap` (see
[First-run bootstrap](#first-run-bootstrap-http)); after that, both surfaces do
the same operations, so pick whichever fits.

---

## Admin authorization model

Administration is gated by the built-in **relationship-based authorization
engine** (a Zanzibar-style tuple ACL), not by a hard-coded list of emails.

- A principal is an admin **if and only if** it holds the global-admin grant
  `system:memory#admin`. The MCP admin gate is a single relationship check:
  `Check(system:memory, admin, <subject>)`. If the subject does not hold that
  relation, the tool returns an error.
- The old email-allowlist gate has been **removed**. `ADMIN_ALLOWED_EMAILS`
  still exists, but only as a **bootstrap seed**: at startup, each configured
  email that maps to a known user is granted `system:memory#admin`. After
  startup, admin status is pure tuple state — you add or remove admins by
  writing or deleting `system:memory#admin` grants, not by editing an env var.
- Global admins reach every tenant through a seeded parent edge
  (`tenant:<id>#system@system:memory`, written when a tenant is created), so a
  holder of `system:memory#admin` is authorized against tenant-scoped and
  document-scoped relations everywhere without a per-tenant grant.
- **Read aggregation is bounded — an admin is not a read firehose.** Memory reads
  (`search_memory`, `list_documents`, `get_document`, `get_document_by_id`) span
  the caller's **readable tenant set**: their home tenant, the common (`default`)
  pool, and every tenant they hold a **direct**
  `viewer`/`member`/`manager`/`admin` grant on (each confirmed by a `Check`). This
  set is derived from **direct** grants only — a `system:memory#admin` holder is
  **not** expanded to every tenant for aggregation, so an admin's search is not an
  instance-wide dump. Admins still reach any single tenant explicitly (through the
  Tenants page, or an admin `tenant_id` scope on the request), just not implicitly
  through a broad search. Each result is labeled with its owning tenant. **Writes
  are unaffected** — stores, updates, and deletes still target one tenant.
---

## First-run bootstrap (HTTP)

On a brand-new instance, the server provisions the first tenant and admin API
key itself, over HTTP, so a fresh instance needs no direct database access. This
is unrelated to `ADMIN_ALLOWED_EMAILS` (which only seeds an admin grant for an
existing user at every startup): first-run bootstrap creates the tenant and key
from nothing.

The tenant provisioned this way is the founding admin's **personal tenant** —
their own private shelf, created with `type=personal` — not the shared `default`
common pool (which stays the shared/public shelf). Bootstrap accepts an optional
**name** for this founding tenant from the setup page's name field; when omitted
it defaults to a sensible value (derived from the admin email), so an unnamed
bootstrap still works.

There is no environment variable to set for this. Instead, on every boot
where no admin exists yet, the server generates a random one-time token
and logs it at `WARN` — read it with `docker logs` (or your log aggregator).
The token is regenerated every boot while the instance stays un-bootstrapped,
held only in memory, and never persisted to disk; once an admin exists it is
no longer generated or logged, and the network-path token compare is
constant-time.

Bootstrap only provisions when **both** hold:

- no admin API key already exists (it is **one-shot** — once an admin exists,
  every further bootstrap attempt is rejected, regardless of token validity),
  and
- the caller presents a token matching the one the server logged this boot.

The returned admin API key is shown **exactly once** and is never written to
application logs.

While the instance is un-bootstrapped, `GET /bootstrap` serves a dedicated,
self-contained setup page (styled like `/ui` but with no vendor JS). It always
shows OAuth status and offers two modes: **MCP tokens** (just provision the
admin Bearer key) and **OAuth** (additionally seed a founding admin email so a
human can sign in to `/ui`). Paste the token logged at boot, pick a mode, and
submit; it calls `POST /bootstrap` below and displays the returned key once.
Once an admin exists, every route under `/bootstrap` — the page,
`POST /bootstrap`, and `GET /bootstrap/config.json` — returns **404**; bootstrap
is one-shot and the whole surface vanishes once armed.

When OAuth is enabled, the server auto-registers the public PKCE `/ui` OAuth
client on boot (from `MEMORY_UI_CLIENT_ID`, defaulting to `memory-ui`, and
`PUBLIC_BASE_URL`), so browser login works with no manual `oauth_clients`
insert. (Configuring the OAuth provider credentials from the page itself,
persisted to the DB, is planned; today the provider is set via the environment.)

Equivalently, call the endpoint directly:

```bash
curl -X POST https://mem.example.org/bootstrap \
  -H 'Content-Type: application/json' \
  -d '{"token": "<token from docker logs>", "admin_email": "alice@example.com"}'
# -> {"api_key":"mmcp_XXXXXXXX...","tenant_id":"...","key_id":"..."}
```

(`POST /bootstrap` also accepts a form-encoded body with `token` and
`admin_email` fields, for a plain `curl -d` without `Content-Type: application/json`.)

`admin_email` is optional. The setup page and the `POST /bootstrap` body also
accept an optional **founding tenant name** (see above), which defaults sensibly
when omitted. Bootstrap issues the founding key with a default label and no
expiry — the `POST /bootstrap` body takes neither. To set a custom label or a
TTL, issue a fresh key afterward with `create_api_key` (`label`, optional
`expires_in_days`) and revoke the founding one. When `admin_email` is set **and**
OAuth login is configured (`MEMORY_MCP_GOOGLE_CLIENT_ID` /
`MEMORY_MCP_GOOGLE_CLIENT_SECRET`), that email is granted `system:memory#admin`
— the same authority as the founding API key — so the operator can sign in at
`/ui` over OAuth and immediately reach every admin function (tenant, user/ACL,
and key management). The field is ignored when OAuth isn't configured; the setup
page then hides it and shows a note that `/ui` stays unavailable until OAuth is
set up.

---

## Self-serve signup and the domain gate

Once an instance has OAuth configured (`MEMORY_MCP_GOOGLE_CLIENT_ID` /
`MEMORY_MCP_GOOGLE_CLIENT_SECRET`), it provisions users **on demand**. The first
time a verified OAuth/OIDC identity logs in with no existing mapping, the server
auto-creates a **personal tenant** for it and makes the user its `tenant#owner`
— their own private shelf. Identity is anchored on the OIDC `(issuer, subject)`
pair, so later logins resolve to that same tenant even after the user's email
changes; email is a mutable attribute refreshed from the claim on each login. A
legacy or admin-seeded email-only mapping adopts the subject on its owner's first
login. An unverified or empty email is refused (403) and nothing is provisioned.

> **Warning — signup is PUBLIC by default.** When `SIGNUP_ALLOWED_DOMAINS` is
> unset or empty, **any** verified identity your IdP will authenticate can
> self-provision a tenant. With Google as the provider that means *any Google
> account on the internet*. An OAuth-enabled instance with no allow-list is an
> open-signup instance.

To lock signup to your organization, set `SIGNUP_ALLOWED_DOMAINS` to a
comma-separated list of email domains:

```bash
SIGNUP_ALLOWED_DOMAINS=example.com,corp.example.com
```

With a non-empty list, only verified emails whose domain matches an entry may
provision — matching is case-insensitive, and Google's `hd` hosted-domain claim
is honored when present. The gate **fails closed**: a disallowed identity is
refused with `403` and nothing is created.

Two things the gate does **not** do:

- It never touches **already-provisioned** users. Existing members keep logging
  in regardless of the allow-list, so tightening it later locks out only *new*
  signups.
- It is not the first-admin gate. The founding admin is provisioned separately
  and is gated by the one-time
  [bootstrap token](#first-run-bootstrap-http); auto-provision
  only applies to post-bootstrap logins.

Instances with no OAuth configured (API-key-only) have no self-serve signup at
all — users are created only by an admin.

---

## Defaults for new tenants

Every newly created tenant — the bootstrap founding tenant, an auto-provisioned
personal tenant, and any tenant an admin creates — is initialized with a
**safe-by-default bundle**:

- **`staleness_mode=hard`** — a recalled record older than its doc_type's
  **expiration age** is **withheld** from results until it is re-verified, while one
  past its (shorter) **verification age** is still served with a re-verify nudge.
  Withholding is age-based only (no content inspection). Recall-time signal only —
  it never deletes. Expiration is opt-in per doc_type (unset ⇒ nothing withheld).
- **`cleanup_scan_enabled=true`** — the nightly scanner walks this tenant for
  near-duplicate candidates, enqueuing each pair into the review queue
  (`get_cleanup_queue`). It no longer archives or deletes on a timer.
- **`duplicate_guard=true`** — a `store_memory` that is a near-duplicate of an
  existing record is refused, steering the client to `update_section` the
  existing record instead of accumulating competing copies. The match cutoff is
  the global `0.85` threshold (or a per-tenant override).

These defaults come from `MEMORY_DEFAULT_OPTS` (the bootstrap seed) and can be
changed live on the admin config page (`/ui/admin`); either way they apply
**only at tenant-create time** — existing tenants keep whatever settings they
already have. See [Configuration](../README.md#configuration).

### This bundle depends on the client verifying on recall

Hard staleness **withholds** an expired record from recall until it is re-verified —
the server does not delete it. So an actively-used document keeps surfacing only
if **the assistant re-verifies or updates it when it reads it**. The memory tools
support this directly: `mark_verified` after confirming a record still holds,
`update_section` when it has drifted. Each verify or update resets the record's
freshness clock. `mark_verified` is the only path an agent has to an expired
record; `force_read` is an **admin-only** break-glass peek that reveals the body
once without resetting the clock — a non-admin `force_read` leaves it withheld.

So the safe bundle is only half a server-side setting; the other half is a
client-side habit. Make sure the assistant that talks to this instance is told
to verify-on-recall, and to retire outdated knowledge deliberately — supersede,
update, or delete — since no automated sweep runs by default. The ready-to-paste
[memory usage prompt](memory-usage-prompt.md) already includes exactly this
guidance — drop it into your agent's system prompt.

### Retention: agent-driven by default, opt-in sweep

By default there is **no automated time-based deletion** — nothing archives or
deletes documents on a timer. Knowledge retirement is **agent-driven** and
deliberate: supersede an entry (a `supersedes` edge archives its target and purges
its content, leaving a lineage-only tombstone — the archived content is not
recoverable), update it in place, or delete it. The staleness setting above is only
a **recall-time signal** — `advisory` warns, `hard` withholds — and never deletes.

An operator can additionally enable a **retention sweep** (`retention_sweep_enabled`,
default off; config page or `RETENTION_SWEEP_ENABLED`). When on, the cleanup scanner
hard-deletes documents that are both expired past their doc_type age and access-cold.
A document is eligible only when its liveness clock —
`GREATEST(last verification, last access, creation)` — is older than the doc_type's
`expiration_age_days` plus the global `retention_grace_days` (default 30), it is not
pinned, and its doc_type has `expiration_age_days > 0`. A document read or re-verified
during the grace window bumps that clock and survives, so the grace window and the
access gate are the safety net. Two escape hatches keep a document forever: a
per-document **pin**, and a doc_type's `expiration_age_days = 0` (never-expire, hence
never eligible — this is how you exempt a whole category).

The sweep rides the cleanup scanner but is gated independently of the near-duplicate
cleanup: a cycle runs when `cleanup_enabled` **or** `retention_sweep_enabled` is on.
The delete is a **hard delete** — the supersede purge cascade (sections, embeddings,
FTS rows, edges) plus a `deletion_events` audit row (reason `retention_sweep`) — and
is **not recoverable**. Preview it first: the `lint_memory` `retention_candidate`
finding runs the candidate query without deleting, regardless of toggle state, so you
can see the blast radius before flipping the toggle on.

Staleness also feeds retrieval **ranking**: the global `staleness_penalty`
(config page / `MEMORY_STALENESS_PENALTY`, default `0.2`, `0` = off) down-weights
a doc verified past its `doc_type` **verification age** so it ranks below
equally-relevant fresh ones, down to `1-penalty` at 2× that age. This re-ranks
only — a down-weighted doc still appears, just lower; nothing is deleted. Ranking
rides the verification age, independent of the tenant mode and of the expiration age.

---

## Doc-type policies

Every document has a `doc_type`, derived from its category (and, for `projects`,
its slug) — `reference`, `project_state`, `audit`, `learning`, `preference`,
`tool`, `journal`, `handoff`. A `doc_type_policies` row per type decides how it
behaves. Policies are **instance-wide** (they apply to every tenant) and edited
only by an **instance admin**, through `set_doc_type_policy` / `PATCH
/api/admin/doc-type-policies`. Editing the table with raw SQL is unsupported: it
bypasses validation and the in-process reload.

Each rule is a nullable column; **NULL means "inherit from the `reference`
row"**, which sets a value for every column. NULL is kept distinct from a set
value — `verification_age_days: 0` means *never nudge*, which a NULL must not
collide with. Values are validated against the *merged* result, so an edit that
would produce a bad combination through inheritance is rejected — including
`expiration_age_days` below `verification_age_days`.

| Rule | Meaning |
| --- | --- |
| `verification_age_days` | Soft-nudge threshold in days; `0` = never nudge |
| `expiration_age_days` | Hard-withhold threshold in days (hard mode only); `0`/NULL disables; must be `>= verification_age_days` |
| `duplicate_guard` | Write-time near-duplicate check runs (requires `write_mode: replace`) |
| `cleanup_scan` | The near-duplicate scanner considers this type |
| `lint_stale_check` | `lint_memory` reports this type as stale |
| `embed` | Sections get embeddings on write |
| `default_search` | Appears in `search_memory` when no `category`/`doc_type` is named |
| `prunable` | Retention may ever remove documents of this type |
| `write_mode` | `replace` \| `merge_sections` \| `append_only` |
| `slug_format` | `any` \| `date` \| `datetime` \| `kebab` — validated, never rewritten |
| `subcategory` | `optional` \| `required` \| `forbidden` |
| `rules` (JSONB) | Structured/experimental rules, e.g. `chain_previous` |

`embed` and `default_search` are separate: the keyword arm of search matches text
with no embedding, so hiding a type from unfiltered results needs
`default_search: false` on both arms — while it can still keep `embed: true` and
rank semantically when a query names its category. `embed: false` is **not
retroactive**: flipping it true later leaves existing documents unembedded until
they are re-stored.

`write_mode` governs what a whole-document store does to existing sections:
`replace` deletes and rewrites (the default); `merge_sections` upserts by heading
and leaves unlisted sections alone (journals use this, so one store per entry
doesn't wipe the day); `append_only` is merge but rejects a heading collision.
The `put_section` tool (`PUT /api/sections`) upserts one section by path+heading
through the same path — creating the document if absent, never truncating others,
never running the duplicate guard.

The seed reproduces prior behavior except three deliberate changes: `journal` and
`handoff` drop out of unfiltered search (`default_search: false`), `journal`
merges sections instead of truncating, and `journal` requires a `YYYY-MM-DD` slug.

## Document includes

Any document can pull in other documents. Link them with an `includes` edge
(`link_documents(parent, child, "includes")`), then read the parent with
`expand=true` (on `get_document` / `get_document_by_id`): the response gains an
`includes` list — the referenced documents, resolved transitively, in link order,
each unique document once — plus an `include_manifest` recording every edge's
outcome. Absent or false `expand`, a read is unchanged. It is doc-type-neutral:
prompts, handoffs, project state, anything.

Resolution is safe by construction — every include is read through the caller's own
read path, so an include the caller could not read directly is never surfaced.
Skipped includes are recorded in the manifest with a reason (`skipped_scope`,
`skipped_cycle`, `skipped_depth`, `skipped_missing`) and contribute no content.
Cycles terminate; the walk is depth- and size-bounded.

**Conditional includes via `scope`.** A document's `scope` (empty = always applies;
otherwise a space-separated glob list) gates whether an include resolves: pass a
`scope` argument on the expand read, and an included document whose scope is
non-empty resolves only when it matches (exact or glob). One root document thus
assembles different results for different contexts, with no per-edge condition.

## Agent prompts

An agent's operating instructions can live in the same tenant as its knowledge,
under `prompts/<agent>/<slug>` — `<agent>` is the consuming agent (the required
subcategory, e.g. `derpy`), `<slug>` names one instruction document (`persona`,
`no-slop`). These are doc_type `prompt`, and the seeded `prompt` policy row exempts
them from **all** curation: never stale/withheld, never duplicate-guarded, never
scanned or linted or pruned, not embedded, and absent from unfiltered
`search_memory` (pass `category=prompts` or `doc_type=prompt` to include them).

**Own-tenant only.** A prompt document is instruction text the agent executes, so
prompts resolve from the caller's home tenant alone — never the common pool, never
a granted tenant — on every read path. Cross-tenant grants cannot reach a prompt.

**Scope.** `scope` (set via `store_memory`, returned on reads, allowed on any
doc_type) decides when a document applies:
- **Empty** → always applies.
- **Non-empty** → a space-separated pattern list (project name or glob, matched
  exact-or-glob against a read-time scope). Situational instructions.

**Retrieval — one root, `get_document expand=true`.** Model an agent's prompt as a
root document under `prompts/<agent>/` that `includes` its parts (persona, no-slop,
workflow, situational rules); read it with `expand=true` and the session `scope`.
The response assembles the always-apply parts plus any scoped part whose `scope`
matches, and the manifest names what was skipped — see [Document includes](#document-includes).

### Client contract (claude-hook-engine)

- Read the agent's root document with `get_document expand=true` at session start,
  passing the project/dir scope.
- Render the assembled always-apply content to disk and keep it @-imported; inject
  the scoped parts as session context.
- Cache the root's `content_hash`; when it and its includes' hashes match the
  cache, skip the fetch and the disk writes.

## Live global configuration

Runtime-tunable globals — retrieval tuning, the new-tenant defaults above,
self-service, the maintenance schedule, the retention-sweep fields
(`retention_sweep_enabled`, `retention_grace_days`, `metrics_retention_days`),
HTTP hardening, log level, the near-duplicate threshold, and the cleanup webhook —
are stored in the `instance_config` singleton and edited **live**, with no restart,
on the admin config page at **`/ui/admin`** (system admins only). The matching environment
variables (see [Configuration](../README.md#configuration)) **seed** these
columns on first migrate; from then on the stored value wins.

Most edits apply immediately; `cleanup_interval_hours` takes effect at the next
scheduler fire. Two globals have **no env var** and are set only here:

- **Near-duplicate threshold** — the global default cosine cutoff for the
  duplicate guard (`0.85`), with an optional per-tenant override (unset ⇒ inherit
  the global default).
- **Cleanup webhook URL** — the near-duplicate cleanup scan POSTs a JSON summary
  to it on each run; empty disables it. This replaces the earlier Telegram
  notifier.

`ADMIN_ALLOWED_EMAILS` seeds the config page's admin-emails field — it grants
`system:memory#admin` at startup, so an edit applies on the next restart. Live
grant/revoke is via the Tenants admin UI.

---

## Usage metrics

Each tenant can opt into a usage-metrics event log with **`metrics_enabled`** (default
off). Set it per tenant on `PATCH /tenants/{id}/settings`, the Tenants admin UI toggle,
or the `update_my_tenant_settings` MCP tool. It rides the same self-service gate as the
other tenant settings: an admin sets it for any tenant, a tenant manager only where the
tenant's self-service policy is `open`, admin-only otherwise.

When on, the tenant appends to the append-only `metric_events` log on each **`access`**
(distinct documents served by a search), **`verify`** (`mark_verified`), and
**`cleanup`** (a retention-sweep delete). All emitters are best-effort and off the
critical path — a metrics write never breaks a read, verify, or delete. The cleanup
scanner prunes rows older than `metrics_retention_days` (default 90) on each cycle,
alongside the mutation-history prune. Stale and expired counts are **live gauges**
(computed COUNT queries over current section state), not events.

`GET /api/admin/metrics` (system admins only) returns a JSON summary — event counts
over the window, the stale/expired gauges, and the top-accessed documents. It takes
`days` (or `window`, default 30) and `top` (default 10) query params. The admin
**Metrics** dashboard page (`/ui`, `#metrics`) renders this summary. The aggregation is
Prometheus-shaped — counters keyed tenant × doc_type × event_type, gauges tenant ×
doc_type, no per-document labels — so a `/metrics` exposition endpoint over the same
series can be added later without rework; it is not shipped yet.

---

## Break-glass reset

`MEMORY_RESET` is an operator-only, boot-time-only escape hatch: there is no
network route that can trigger it. Set it when starting the server:

```bash
MEMORY_RESET=1 memory-mcp
```

On startup, this clears the admin API key(s) and their `system:memory#admin`
authorization tuple(s) and re-arms bootstrap — **it never deletes tenants,
documents, or memories**. After the reset, `GET /ui` goes back to returning
**404** (no admin exists again), `/bootstrap` reappears, and that same
startup logs a freshly generated bootstrap token — none is persisted across
boots, so re-bootstrapping needs whatever token this boot printed. Unset
`MEMORY_RESET` before the next restart, or every boot will reset again.

---

## Importing documents

Load a corpus of markdown documents — each parsed from its path into
`category/subcategory/slug` — via the admin UI or the admin HTTP API. Both
front-ends feed the same ingest core (`service.ImportDocuments`) and bypass the
near-duplicate guard (bulk import is assumed intentional).

### Admin UI

The admin page at `/ui` includes an upload view: pick an archive, submit, and
watch it progress to `succeeded` (or `failed`) via the same status polling
described below.

### HTTP API

`POST /api/admin/import` (admin-authenticated) accepts a **zip** archive as a
multipart form upload in the `archive` field:

```bash
curl -X POST https://mem.example.org/api/admin/import \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -F archive=@corpus.zip
# -> 202 {"id":"...","status":"queued"}
```

**Each file's path inside the zip decides where it lands** —
`category/slug.md` or `category/subcategory/slug.md` (subcategory optional).
Put those category folders at the top level of the zip: don't wrap them in a
parent folder, or that folder is read as the category and every entry lands
under the catch-all `misc` category (`wrapper/category/subcategory/slug.md` no
longer parses as `category/subcategory/slug`). Zip the contents of your export
directory, not a parent directory containing it.

Import is **asynchronous**: the request returns immediately with a job id,
and an in-process worker (concurrency set by `IMPORT_WORKER_CONCURRENCY`)
drains the queue in the background. Poll status with:

```bash
curl https://mem.example.org/api/admin/import/<id> \
  -H "Authorization: Bearer $ADMIN_TOKEN"
# -> {"id":"...","tenant_id":"...","status":"running","total":42,
#     "imported":30,"skipped":1,"failed":0, ...}
```

`status` is one of `queued`, `running`, `succeeded`, `failed`. A job is
visible only within the tenant that owns it — an unknown or cross-tenant id
returns 404. If the server restarts mid-import, the interrupted job is marked
`failed` on the next boot rather than sticking in `running` — re-upload to
retry.

The upload is capped by `IMPORT_MAX_UPLOAD_BYTES` (default 32 MiB). This is
deliberately its own limit: the global `MAX_REQUEST_BYTES` cap does not apply
to this route, since a corpus archive is expected to be larger than a typical
API request.

---

## Provisioning more tenants and keys

After the first admin exists, create additional tenants and their keys from
either surface — the web console's Tenants page, or the MCP admin tools. The
tool sequence is:

1. **Create a tenant** with `create_tenant` (`name`); note the returned tenant
   id.
2. **Issue an API key** for it with `create_api_key` (`tenant_id`, `label`, and
   an optional `expires_in_days` TTL). The plaintext key is returned **once** —
   copy it now; it is never retrievable again. Hand `mmcp_...` to the MCP client
   as its bearer token.
3. **(Optional) Grant a human admin** for the OAuth / browser path with
   `grant_user` (`email`, `tenant_id`, `role: admin`), so a person can sign in at
   `/ui` and manage the instance.

Ordering only matters in that the tenant id from step 1 feeds steps 2 and 3.

---

## Managing users

A "user" is an OIDC identity mapped to exactly one tenant, with a role of
`member` or `admin`. Identity is anchored on the `(issuer, subject)` pair; email
is a mutable attribute (still **globally unique** — one email maps to one
identity) that refreshes on login and is no longer the lookup key. Granting an
email that already exists is **refused** (with a hint to update the role or
revoke first); re-home a user by revoking and re-granting.

A grant is not just a database row. It keeps two things in sync automatically:

1. the `tenant_users` mapping (identity → tenant, role), and
2. the authorization membership tuple(s): every user gets
   `tenant:<id>#member`, and an `admin`-role user *additionally* gets
   `tenant:<id>#admin`.

Changing a role or revoking a user updates the tuples in lockstep — setting
`admin` writes the admin tuple, demoting to `member` deletes it, and revoking
deletes all of the user's membership tuples. You never touch tuples by hand for
user management.

| Operation | MCP tool | Parameters |
|---|---|---|
| Grant access | `grant_user` | `email`, `tenant_id`, `role` (`member` default \| `admin`) |
| List users | `list_users` | `tenant_id` |
| Change role | `update_user_role` | `email`, `role` (`member` \| `admin`) |
| Revoke access | `revoke_user` | `email` |

`role` defaults to `member` when omitted on grant. The web console's Tenants page
does the same operations.

---

## API key lifecycle (the token cycle)

API keys are `mmcp_`-prefixed random tokens, SHA-256 hashed at rest. The
plaintext is returned **once**, at issue or rotate time, and never stored or
retrievable afterward. Listing a key shows metadata only — id, label, 8-char
display prefix, created / last-used / expiry timestamps, and revoked status —
never the secret.

### Issue

`create_api_key` (`tenant_id`, `label`, optional `expires_in_days`) issues a key
for a tenant. Omit `expires_in_days` for a key that never expires. The plaintext
is returned once.

### Rotate

`rotate_api_key` (`key_id`, optional `grace_hours`) is a single operation that
issues a replacement key (same tenant and label) and retires the predecessor.

- The replacement's plaintext is **returned once** — capture it immediately.
- **Without `grace_hours`**, the predecessor is **revoked immediately**; any
  client still presenting the old key starts failing at once.
- **With `grace_hours`**, the predecessor's expiry is set to `now + grace` and it
  stays valid until then, so clients can swap to the new key with **no downtime**.

### Revoke

`revoke_api_key` (`key_id`) revokes a key immediately.

### Expiry semantics

- A key past its `expires_at` fails authentication **exactly as if it were
  invalid** (validation requires `expires_at IS NULL OR expires_at > now()`).
- A key issued with **no** TTL has a null expiry and **never expires** — it
  only stops working when explicitly revoked (or rotated without grace).
- Revocation and expiry are independent: a revoked key is dead regardless of
  its expiry, and an expired key is dead regardless of its revoked flag.

---

## Runbook: rotate a leaked key with zero downtime

A key has leaked but is still in active use by a client you control. You want
the old key dead soon, but without an outage while you redeploy.

1. **Rotate with a grace window** long enough to redeploy every client: call
   `rotate_api_key` with the leaked `key_id` and `grace_hours: 24`. This returns
   the new plaintext once and sets the leaked key to expire in 24 hours. Both
   keys authenticate during the window.

2. **Deploy the new key** to all clients within the window (update the secret
   in your secret manager and roll the consumers).

3. **Wait for the window to elapse.** The leaked key auto-expires at
   `now + 24h` and thereafter fails authentication like any invalid key — no
   manual revoke step, no missed cutover.

> If the leak is actively being exploited and downtime is acceptable, skip the
> grace window entirely: call `rotate_api_key` with just the `key_id` to revoke
> the old key immediately, then deploy the replacement.

## Running in production

A pre-flight checklist before exposing an instance; each item links the fuller
explanation elsewhere.

- **Terminate TLS in front of the server.** It speaks plain HTTP — run it behind
  a reverse proxy or ingress that holds the certificate.
- **Set `PUBLIC_BASE_URL`** to the external origin (absolute `https://…`, no
  trailing slash) whenever OAuth is enabled: it anchors the issuer, callback, and
  UI OAuth config, and the server refuses to start without it on the OAuth path.
- **Set `RATE_LIMIT_TRUSTED_PROXY_DEPTH`** to the number of proxy hops in front of
  the server. The default `0` keys the limiter on the socket peer and ignores
  `X-Forwarded-For` (unspoofable); a wrong value either trusts a spoofable header
  or rate-limits every client as one.
- **Lock signup.** An empty `SIGNUP_ALLOWED_DOMAINS` lets anyone with a verified
  upstream identity self-provision a tenant — set it to your domains, or keep
  OAuth signup off. See [Self-serve signup](#self-serve-signup-and-the-domain-gate).
- **Generate and keep `AUTHLET_MASTER_KEY`** (32-byte hex). It encrypts the OAuth
  authorization-server signing material at rest; back it up, and treat losing it
  as invalidating every issued OAuth token.
- **Use `sslmode=require`** (or stricter) in `DATABASE_URL`. The bundled compose
  default is `sslmode=disable`, for local use only.
- **Seed the founding admin** via `ADMIN_ALLOWED_EMAILS` or the one-shot
  [`/bootstrap`](#first-run-bootstrap-http) flow; admin status is then tuple state
  you manage from the console.
- **Tune the edge** if needed: `MAX_REQUEST_BYTES` (default 1 MiB) caps request
  bodies; `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` throttle the auth + write surface.
- **The container already runs non-root** (`distroless/static:nonroot`, uid
  65532) — keep it that way if you rebuild.
- **Back it up** — see below.

## Backup, restore, and upgrade

The server is stateless; **all durable state lives in PostgreSQL** — documents,
section embeddings, tenants, users, API keys, authorization tuples, and the
runtime config. Back up the database and the master key; the container needs
nothing else.

### Back up

- **Database**: schedule a `pg_dump` of the memory database, or snapshot its
  volume. Everything the instance knows is in there.
- **`AUTHLET_MASTER_KEY`**: keep it in your secret manager. It is not in the
  database, and a restored instance can't decrypt its OAuth signing material
  without the original.
- API-key plaintexts are shown once and stored only as hashes, so they are **not**
  recoverable from a backup by design — reissue rather than recover.

### Restore

1. Bring up a fresh **pgvector** Postgres (`CREATE EXTENSION vector` must be
   available — plain postgres won't do).
2. Load the `pg_dump` into it.
3. Start the server pointed at it with the **same `AUTHLET_MASTER_KEY`**.
   Boot-time schema setup (`AutoMigrate` plus the structural indexes and seeds) is
   idempotent, so it no-ops against the restored schema.

### Upgrade

1. Pull the new image and restart the server; on boot it runs the same idempotent
   schema setup, then serves.
2. Verify: `GET /~/version` reports the new version/commit and `GET /~/ready`
   returns healthy.
3. Schema changes are additive; **downgrading the image is not supported** once a
   newer version has run. Take a backup before a major upgrade.

> **The embedding model is a one-way door.** Changing `EMBEDDING_PROVIDER`, the
> model, or `EMBEDDING_DIMENSIONS` changes the vector dimension, and the server
> refuses to start against a corpus built with a different one. Switching means
> re-embedding the whole corpus — see [Embedding providers](embedding-providers.md).
