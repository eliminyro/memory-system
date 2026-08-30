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

- **`staleness_mode=hard`** — a recalled record that has gone stale *and*
  references something that can change (a code path, a version, a config) is
  **withheld** from results until it is re-verified. This is a recall-time signal
  only — it never deletes.
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

Hard staleness **withholds** a stale record from recall until it is re-verified —
the server does not delete it. So an actively-used document keeps surfacing only
if **the assistant re-verifies or updates it when it reads it**. The memory tools
support this directly: `mark_verified` after confirming a record still holds,
`update_section` when it has drifted. Each verify or update resets the record's
staleness clock.

So the safe bundle is only half a server-side setting; the other half is a
client-side habit. Make sure the assistant that talks to this instance is told
to verify-on-recall, and to retire outdated knowledge deliberately — supersede,
update, or delete — since no automated sweep will do it. The ready-to-paste
[memory usage prompt](memory-usage-prompt.md) already includes exactly this
guidance — drop it into your agent's system prompt.

### No automated retention sweep

There is **no automated time-based deletion**. Earlier versions ran a staleness
archive→hard-delete sweep and a separate global access-recency eviction; both
have been **removed**. Nothing archives or deletes documents on a timer.

Knowledge retirement is **agent-driven** and deliberate: supersede an entry (a
`supersedes` edge archives its target and purges its content, leaving a
lineage-only tombstone — the archived content is not recoverable), update it in
place, or delete it. The staleness setting above is only a **recall-time
signal** — `advisory` warns, `hard` withholds — and never deletes.

Staleness also feeds retrieval **ranking**: the global `staleness_penalty`
(config page / `MEMORY_STALENESS_PENALTY`, default `0.2`, `0` = off) down-weights
a doc verified past its `doc_type` threshold so it ranks below equally-relevant
fresh ones, down to `1-penalty` at 2× the threshold. This re-ranks only — a
down-weighted doc still appears, just lower; nothing is deleted.

---

## Live global configuration

Runtime-tunable globals — retrieval tuning, the new-tenant defaults above,
self-service, the maintenance schedule, HTTP hardening, log level, the
near-duplicate threshold, and the cleanup webhook — are stored in the
`instance_config` singleton and edited **live**, with no restart, on the admin
config page at **`/ui/admin`** (system admins only). The matching environment
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
