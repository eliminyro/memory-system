# Administering your instance

This guide covers day-to-day operation of a running memory instance: creating
tenants, granting people access, and issuing/rotating/revoking API keys.

There are two equivalent administrative surfaces:

- **The `memory-admin` CLI** — operates *directly against the database* via
  `DATABASE_URL`. It drives the repository layer with no network hop, so it
  works even when **no server is running**. This is the only way to bootstrap
  the very first tenant and API key on a fresh instance.
- **The MCP admin tools** — the same operations exposed to an already-privileged
  MCP client (for example, an agent whose key belongs to a global admin, or a
  human logged in over OAuth). Use these once the instance is up and you have a
  working admin credential.

Every operation below is available from both surfaces. Pick whichever fits: the
CLI for bootstrap, scripting, and break-glass; the MCP tools for ongoing
management from a client you already have wired up.

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
- The **`memory-admin` CLI is inherently privileged**: holding `DATABASE_URL`
  is equivalent to full control of the instance. It performs no authorization
  check of its own and needs no token — protect the database URL accordingly
  and treat the CLI as an operator tool, not a user tool.

---

## First-run bootstrap (HTTP or CLI, one step)

On a brand-new instance, the server can provision the first tenant and admin
API key itself — over HTTP or with a single CLI command — instead of the
manual tenant/key dance further down. This is unrelated to
`ADMIN_ALLOWED_EMAILS` (which only seeds an admin grant for an existing user
at every startup): first-run bootstrap creates the tenant and key from
nothing.

The tenant provisioned this way is the founding admin's **personal tenant** —
their own private shelf, created with `type=personal` — not the shared `default`
common pool (which stays the shared/public shelf). Bootstrap accepts an optional
**name** for this founding tenant: supply it from the setup page's name field or
the CLI's `--tenant-name` flag. When omitted it defaults to a sensible value
(derived from the admin email), so an unnamed bootstrap still works.

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
- the caller presents a token matching the one the server logged this boot
  (the network path only — see "From the CLI" below for the token-free
  offline path).

The returned admin API key is shown **exactly once** and is never written to
application logs.

### Over HTTP

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
when omitted; the key label and a key TTL stay CLI-only, so use `memory-admin
bootstrap` or the manual CLI flow further down for a custom key label or a key
expiry. When `admin_email` is set **and**
OAuth login is configured (`MEMORY_MCP_GOOGLE_CLIENT_ID` /
`MEMORY_MCP_GOOGLE_CLIENT_SECRET`), that email is granted `system:memory#admin`
— the same authority as the founding API key — so the operator can sign in at
`/ui` over OAuth and immediately reach every admin function (tenant, user/ACL,
and key management). The field is ignored when OAuth isn't configured; the setup
page then hides it and shows a note that `/ui` stays unavailable until OAuth is
set up.

### From the CLI

```bash
memory-admin bootstrap --tenant-name acme --key-label acme-admin
# -> prints the plaintext admin key to stdout, once
```

`memory-admin bootstrap` needs no token: it talks to the database directly via
`DATABASE_URL`, runs under a local-admin context, and bypasses the token gate
entirely (holding `DATABASE_URL` is already full control). It drives the same
one-shot provisioning core as the HTTP endpoint and exits non-zero if an admin
already exists. Unlike the HTTP path it does **not** take an admin-email flag
— grant a human OAuth admin afterward with `memory-admin user grant` (see
"Managing users" below). (`memory-admin setup` is a different, unrelated
command that emits an `mcpServers` client config — see
[`connecting-clients.md`](connecting-clients.md).)

---

## Self-serve signup and the domain gate

Once an instance has OAuth configured (`MEMORY_MCP_GOOGLE_CLIENT_ID` /
`MEMORY_MCP_GOOGLE_CLIENT_SECRET`), it provisions users **on demand**. The first
time a verified OAuth/OIDC identity logs in with no existing tenant membership,
the server auto-creates a **personal tenant** for that email and grants the user
`tenant#admin` on it — their own private shelf. The `email → one home tenant`
invariant holds: later logins with the same verified email always resolve to
that same tenant, never a second one. An unverified or empty email is refused
(403) and nothing is provisioned.

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
  [bootstrap token](#first-run-bootstrap-http-or-cli-one-step); auto-provision
  only applies to post-bootstrap logins.

Instances with no OAuth configured (API-key-only) have no self-serve signup at
all — users are created only by an admin.

---

## Retention defaults for new tenants

Every newly created tenant — the bootstrap founding tenant, an auto-provisioned
personal tenant, and any tenant an admin creates — is initialized with a
**safe-by-default retention bundle**:

- **`staleness_mode=hard`** — a recalled record that has gone stale *and*
  references something that can change (a code path, a version, a config) is
  **withheld** from results until it is re-verified. Hard is also the mode the
  retention sweep acts on.
- **`cleanup_scan_enabled=true`** — the nightly scanner walks this tenant,
  archiving records whose staleness has passed the retention window
  (`RETENTION_MULTIPLIER` × the per-`doc_type` threshold) and hard-deleting them
  after the `RETENTION_DELETE_GRACE_DAYS` grace period.
- **`duplicate_guard=true`** — a `store_memory` that is a near-duplicate of an
  existing record is refused, steering the client to `update_section` the
  existing record instead of accumulating competing copies.

These defaults come from `MEMORY_DEFAULT_OPTS`; override that variable at server
start to change what *new* tenants get (see
[Configuration](../README.md#configuration)). They apply **only at
tenant-create time** — existing tenants keep whatever settings they already
have.

### This bundle depends on the client verifying on recall

Hard staleness plus the cleanup sweep will archive, and eventually delete,
documents that go stale and are never re-verified. That is the point — dead
knowledge gets swept — but it means **an actively-used document survives only if
the assistant re-verifies or updates it when it reads it**. The memory tools
support this directly: `mark_verified` after confirming a record still holds,
`update_section` when it has drifted. Each verify or update resets the record's
staleness clock.

So the safe bundle is only half a server-side setting; the other half is a
client-side habit. Make sure the assistant that talks to this instance is told
to verify-on-recall. The ready-to-paste
[memory usage prompt](memory-usage-prompt.md) already includes exactly this
guidance — drop it into your agent's system prompt so good documents stay alive
instead of being swept.

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
`category/subcategory/slug` — via the admin UI, the admin HTTP API, or the
CLI. All three front-ends feed the same ingest core and bypass the
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

**The documents must sit at the root of the zip** —
`category/slug.md` or `category/subcategory/slug.md`. A wrapping top-level
directory shifts every path down a level, so
`wrapper/category/subcategory/slug.md` no longer parses into
category/subcategory/slug: the whole nested path is instead stored under the
catch-all `misc` category. Zip the contents of your export directory, not a
parent directory containing it.

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

### CLI

For bulk or offline loads (no running server needed, or a k8s Job), walk a
local directory with the `memory-import` binary:

```bash
memory-import --tenant <tenant-uuid-or-name> ~/path/to/memory-directory
```

`--tenant` is required. Since the CLI walks the filesystem directly (not a
zip), ordinary nested subdirectories are fine — the zip-root caveat above
does not apply here. It reports imported/skipped/failed counts on completion.

---

## Bootstrapping a fresh instance (CLI, no server needed)

Prefer the [one-shot bootstrap](#first-run-bootstrap-http-or-cli-one-step)
above for a fresh instance — `memory-admin bootstrap` already covers
independent tenant name/email and key label. Use the manual flow below
instead when you want a key TTL (`memory-admin bootstrap` doesn't take one),
or want the three provisioning steps broken out individually.

On a brand-new instance there are no tenants, no keys, and possibly no server
running yet. Bootstrap entirely from the CLI. Point it at the database first:

```bash
export DATABASE_URL='postgres://user:pass@db.example.org:5432/memory?sslmode=require'
```

**1. Create a tenant** and note the returned tenant id:

```bash
memory-admin tenant create --name "acme"
# -> tenant created: id=6f1c2b0e-...  name=acme
```

**2. Issue an API key** for that tenant. The plaintext key is printed **exactly
once** — copy it now; it is never retrievable again:

```bash
memory-admin key issue --tenant 6f1c2b0e-... --label "acme-agent"
# -> key: mmcp_XXXXXXXX...   (shown once — store it in your secret manager)
```

Hand `mmcp_...` to the MCP client (as its bearer token) and the instance is
usable.

**3. (Optional) Grant a human admin** for the OAuth / browser path. This lets a
person sign in and manage the instance without the CLI:

```bash
memory-admin user grant --email alice@example.com --tenant 6f1c2b0e-... --role admin
```

> Bootstrap ordering only matters here: tenant → key (and optionally → user).
> The tenant id from step 1 feeds steps 2 and 3.

---

## Managing users

A "user" is a human email mapped to exactly one tenant, with a role of `member`
or `admin`. Email is **globally unique** — one email maps to one tenant.
Granting an email that already exists is **refused** (with a hint to update the
role or revoke first); re-home a user by revoking and re-granting.

A grant is not just a database row. It keeps two things in sync automatically:

1. the `tenant_users` mapping (email → tenant, role), and
2. the authorization membership tuple(s): every user gets
   `tenant:<id>#member`, and an `admin`-role user *additionally* gets
   `tenant:<id>#admin`.

Changing a role or revoking a user updates the tuples in lockstep — setting
`admin` writes the admin tuple, demoting to `member` deletes it, and revoking
deletes all of the user's membership tuples. You never touch tuples by hand for
user management.

| Operation | CLI | MCP tool |
|---|---|---|
| Grant access | `memory-admin user grant --email <e> --tenant <id> --role <member\|admin>` | `grant_user` |
| List users | `memory-admin user list --tenant <id>` | `list_users` |
| Change role | `memory-admin user set-role --email <e> --role <member\|admin>` | `update_user_role` |
| Revoke access | `memory-admin user revoke --email <e>` | `revoke_user` |

`--role` defaults to `member` when omitted on grant.

---

## API key lifecycle (the token cycle)

API keys are `mmcp_`-prefixed random tokens, SHA-256 hashed at rest. The
plaintext is returned **once**, at issue or rotate time, and never stored or
retrievable afterward. Listing a key shows metadata only — id, label, 8-char
display prefix, created / last-used / expiry timestamps, and revoked status —
never the secret.

### Issue

Issue a key for a tenant, with an optional TTL. Omit the TTL for a key that
never expires:

```bash
memory-admin key issue --tenant <tenant-id> --label "ci-runner" --ttl-days 90
```

MCP: `create_api_key` with an optional `expires_in_days` (omit for no expiry).

### Rotate

Rotation is a single operation that issues a replacement key (same tenant and
label) and retires the predecessor:

```bash
# revoke the old key immediately (no overlap):
memory-admin key rotate --id <key-id>

# or keep the old key valid for a 24h overlap window:
memory-admin key rotate --id <key-id> --grace-hours 24
```

MCP: `rotate_api_key` with an optional `grace_hours`.

- The replacement's plaintext is **returned once** — capture it immediately.
- **Without grace**, the predecessor is **revoked immediately**; any client
  still presenting the old key starts failing at once.
- **With grace**, the predecessor's expiry is set to `now + grace` and it stays
  valid until then, so clients can swap to the new key with **no downtime**.

### Revoke

```bash
memory-admin key revoke --id <key-id>
```

MCP: `revoke_api_key`.

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

1. **Rotate with a grace window** long enough to redeploy every client:

   ```bash
   memory-admin key rotate --id <leaked-key-id> --grace-hours 24
   ```

   This prints the new plaintext once and sets the leaked key to expire in 24
   hours. Both keys authenticate during the window.

2. **Deploy the new key** to all clients within the window (update the secret
   in your secret manager and roll the consumers).

3. **Wait for the window to elapse.** The leaked key auto-expires at
   `now + 24h` and thereafter fails authentication like any invalid key — no
   manual revoke step, no missed cutover.

> If the leak is actively being exploited and downtime is acceptable, skip the
> grace window entirely: `memory-admin key rotate --id <leaked-key-id>` revokes
> the old key immediately, then deploy the replacement.
