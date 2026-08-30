# memory-system — Architecture

`memory-system` is a self-hostable [Model Context Protocol](https://modelcontextprotocol.io)
(MCP) server that gives AI agents a persistent, semantic long-term memory. Documents
are stored as markdown, split into sections, embedded, and made retrievable through
hybrid (vector + lexical) search backed by PostgreSQL with the `pgvector` extension.

It is a single Go binary (module `github.com/eliminyro/memory-system`) that serves MCP
over HTTP, ships a relationship-based authorization engine, and supports two independent
ways to authenticate: long-lived API keys and OAuth 2.1 / OIDC.

## Major components

All application code lives under `internal/`. The single entry point is in `cmd/`.

| Package | Responsibility |
|---------|----------------|
| `cmd/server` | Process entry point. Loads config, connects and migrates the DB, wires repositories, the embedder, the authz engine, the MCP server, the auth paths, and the HTTP server. |
| `internal/config` | Environment-variable configuration (`caarlos0/env`). |
| `internal/database` | Postgres connection, pool, migrations, `pgvector` setup and indexes. |
| `internal/repository` | GORM data-access layer (documents, sections, tenants, keys, cleanup, overrides). Owns the hybrid-search SQL. |
| `internal/models` | GORM row types. |
| `internal/service` | Business logic (`MemoryService`): embedding orchestration, store/search/update, duplicate guard, staleness application, authorization calls. |
| `internal/mcp` | MCP tool surface and the Streamable-HTTP transport. |
| `internal/authz` | Zanzibar-style relationship-based authorization engine — the single decision point. |
| `internal/authzseed` | Seed/backfill helpers for authz relation tuples and bootstrap admins. |
| `internal/auth` | API-key authentication path and the shared `Subject` principal abstraction. |
| `internal/authletas` | OAuth 2.1 / OIDC Authorization Server (the "authlet"), federating to an upstream IdP. |
| `internal/authletstore` | Persistence for OAuth clients, codes, refresh tokens, and signing keys. |
| `internal/middleware` | CORS, request-size limit, rate limiting, security headers, and the auth-routing chain. |
| `internal/server` | HTTP handler assembly and the embedded web UI. |
| `internal/cleanup` | Periodic scanner: near-duplicate detection, the dead-key sweep, and the outbound webhook notifier. |
| `internal/staleness` | Per-`doc_type` staleness thresholds and the recall-time staleness signal. |
| `internal/globalconfig` | Cached accessor over the DB-backed global `instance_config` (seeded from env, invalidated on admin write). |

## Data model and storage

`internal/database` connects to Postgres via GORM (SQLite is accepted only for unit
tests) and enables `pgvector` (`CREATE EXTENSION IF NOT EXISTS vector`). Migrations run
in a single transaction and, among other things:

- Create documents and sections tables. Each **section** carries an `embedding` column
  of type `vector(N)`, where `N` is the configured embedding dimension (default `768`).
- Freeze the embedding identity (provider + model + dimension) in an
  `embedding_metadata` guard so the server refuses to start if the configured provider,
  model, or dimension silently changes out from under existing vectors.
- Build a **HNSW** vector index (`hnsw (embedding vector_cosine_ops)`) for approximate
  nearest-neighbour search, a GIN index over a generated `tsvector` column for lexical
  search, and a tenant-scoped unique index on document path.

Similarity is **cosine distance** using pgvector's `<=>` operator; relevance is scored
as `1 - (embedding <=> query_vector)`.

All data is **tenant-scoped**. Authorization tuples for the relationship graph live in a
`relation_tuples` table.

## Data flow

### Storing a memory (`store_memory`)

1. The MCP handler validates the request and resolves the target tenant.
2. `MemoryService.StoreDocument` parses the markdown body into sections.
3. Each section is embedded via the configured embedding provider **before any DB
   write**, so a provider failure never leaves a half-written document.
4. An optional duplicate guard runs a similarity query
   (`MAX(1 - (embedding <=> new_vector)) >= threshold`); a near-duplicate returns a
   `similar_exists` result unless the caller forces the write.
5. The document and its embedded sections are inserted, and a
   `document#tenant@tenant` relationship tuple is seeded so the document inherits its
   tenant's access rules.

### Searching (`search_memory`)

1. `MemoryService.Search` embeds the query text.
2. It resolves the caller's **readable tenant set** (see
   [Read scope](#read-scope-aggregated-reads)).
3. The repository runs a **hybrid search** across that set: vector candidates
   ordered by cosine distance are fused with lexical candidates ranked by
   `ts_rank(tsv, plainto_tsquery(...))`.
4. Each result is annotated with its **owning tenant** (id, name, type) and
   filtered according to *that tenant's* staleness mode (unless the caller forces
   a read), then returned.

### Read scope (aggregated reads)

Reads — `search_memory`, `list_documents`, `get_document`, and
`get_document_by_id` — span the caller's **readable tenant set**, not a single
tenant. The set is:

- the caller's **home tenant**,
- the shared **common (`default`) pool**, and
- every tenant for which the caller holds a **direct**
  `viewer`/`member`/`manager`/`admin` grant, each confirmed by an authz `Check`.

The set is built from *direct* grants only. A global admin is **not** expanded to
every tenant for aggregation — that would turn each admin search into an
instance-wide dump. Admins still reach any specific tenant explicitly (through the
Tenants page or the admin write-path tenant override), just not implicitly through
a broad search. The repository filters strictly on this resolved set, so no
document outside it is ever returned.

Every returned item is labeled with its **owning tenant** (id, name, type) so a
client can group or color results by tenant, and staleness is applied using each
result's *own* tenant settings rather than one global mode.

A read may be narrowed to one tenant with an optional **`tenant_id` filter**. The
filter applies only when that tenant is inside the readable set; naming a tenant
the caller cannot read yields an **empty** result, never that tenant's documents.
This read filter is distinct from the admin-only write-path tenant override.

**Writes are unchanged.** `store_memory`, `update_section`, `delete_document`, and
the create paths still target the caller's single authorized tenant — only the
read scope widened.

> **Upgrade note.** This is a behavior change for existing MCP clients. Previously
> `search_memory` and `list_documents` returned only the caller's home tenant (plus
> the common pool); they now aggregate across the full readable set, so an agent's
> search surfaces the user's team and shared-tenant knowledge, not just their
> personal shelf. Writes are unaffected. To pin a read to one tenant, pass the
> `tenant_id` filter.

## MCP surface

`internal/mcp` exposes tools over a stateless Streamable-HTTP transport at `/mcp`. There
is no separate MCP port — MCP rides on the same HTTP listener as everything else.

The server builds **two** tool surfaces sharing one implementation identity. On every
request it evaluates whether the caller is an admin (via the authz engine) and routes to
the admin surface or the regular surface, failing closed to the regular surface when the
subject is missing or the check errors.

- **Regular tools:** `search_memory`, `get_document`, `get_document_by_id`,
  `get_document_history`, `list_documents`, `generate_index`, `get_related`,
  `list_document_edges`, `store_memory`, `update_section`, `delete_document`,
  `delete_section`, `merge_documents`, `link_documents`, `unlink_documents`,
  `lint_memory`, `mark_verified`, `get_cleanup_queue`, `mark_cleanup_done`,
  `update_my_tenant_settings`, and `resume`. Deleting a document's last remaining section
  via `delete_section` also deletes the now-empty parent document.
- **Delegated-ACL tools** (on both surfaces, not admin-only): `grant_tenant_access`,
  `revoke_tenant_access`, `grant_document_access`, `revoke_document_access`,
  `list_tenant_grants`, `list_document_grants`. The service enforces the grant-ceiling and
  fails closed, so a caller who lacks standing is refused there rather than at the tool.
- **Admin tools:** `list_tenants`, `create_tenant`, `update_tenant`, `delete_tenant`,
  `create_api_key`, `list_api_keys`, `revoke_api_key`, `rotate_api_key`, `grant_user`,
  `list_users`, `update_user_role`, `revoke_user`.

Tool handlers are thin: they validate input, resolve the tenant (writes) or the
caller's readable tenant set (reads), and delegate to `MemoryService`.

## Authorization — one engine, two auth paths

Authorization is centralized in `internal/authz`, a Google-Zanzibar-style
relationship-based engine. It stores tuples of the form
`object#relation@subject` and answers `Engine.Check(objectType, objectID, relation,
subjectType, subjectID)` by recursively evaluating a fixed, compile-time namespace
(with a depth limit and cycle guard). The type/relation model covers `user`, `system`,
`tenant`, and `document` objects with relations such as `admin`, `member`, `viewer`, and
`editor`, plus parent edges (`document → tenant → system`) so access inherits downward.

Every access decision — per-request admin routing in the MCP layer, per-document
read/write in the service layer — goes through `Engine.Check`. The engine **fails
closed**: a missing subject or an unwired engine denies access.

The two authentication mechanisms both resolve the caller down to the same principal
abstraction — `auth.Subject{Type: "user", ID: ...}` — placed in the request context, so
the authorization model is identical regardless of how the caller authenticated:

### API-key path (`internal/auth`)

- Keys are issued as an opaque token, stored only as a SHA-256 hash (plaintext shown
  once at creation).
- The API-key middleware extracts the bearer token, hashes it, looks up the active key,
  and injects a `Subject` — either the key's explicit subject or the tenant's service
  principal.

### OAuth 2.1 / OIDC path (`internal/authletas` + `internal/authletstore`)

- When configured, the server stands up an OAuth 2.1 / OIDC Authorization Server that
  **federates to an upstream identity provider** (Google) via OIDC discovery at boot.
- It mounts standard endpoints under `/oauth` (`/authorize`, `/token`, `/register` for
  Dynamic Client Registration, `/idp/callback`, `/revoke`, `/userinfo`) plus the
  well-known documents (`/.well-known/oauth-authorization-server`,
  `/.well-known/openid-configuration`, `/.well-known/jwks.json`, and the RFC 9728
  protected-resource metadata for `/mcp`).
- A **user bridge** maps an authenticated, email-verified identity to an existing tenant
  and subject: `MemoryUserResolver.Resolve` rejects empty, unverified, or unknown emails
  (it never auto-provisions), and `UserContextBridge` middleware attaches the resolved
  tenant and `Subject` to the request context.
- Signing keys are sealed at rest with a master key (AES-256-GCM) and rotated by a
  background cleanup goroutine that also expires codes, refresh tokens, and idle DCR
  clients.

At the HTTP edge, a `DualAuth` router inspects the bearer token: a three-segment JWT is
verified by the OAuth path; anything else falls through to the API-key path. Whichever
path succeeds writes the `Subject`, and the MCP transport then calls `Engine.Check`.

## Staleness and cleanup

Two packages implement lifecycle management:

- `internal/staleness` tracks per-`doc_type` staleness thresholds and a per-tenant
  staleness **mode**, applied only as a **recall-time signal**: `off` (none), `advisory`
  (stale results are surfaced with a warning but usable), or `hard` (stale content is
  withheld unless the caller forces the read). The signal never deletes.
- `internal/cleanup` runs a periodic scanner. Per tenant it performs near-duplicate
  detection, enqueuing candidates into a `cleanup_queue` for review. A companion sweep
  hard-deletes long-dead (revoked or expired) API keys. On each completed scan an optional
  notifier POSTs a JSON summary to the configured `webhook_url` (empty disables).

There is **no automated time-based retention**. The aged-knowledge sweep (the staleness
archive→hard-delete path) and the access-recency eviction have been removed. Knowledge
retirement is **agent-driven**: supersede an entry (a `supersedes` edge auto-archives its
target), update it in place, or delete it. The `archived` state and supersede-driven
archival are kept; only the timers that used to set archival are gone.

## Configuration

The server reads its **boot-only** settings from environment variables (parsed by
`internal/config`); the runtime-tunable globals additionally **seed** the DB
`instance_config` singleton at migrate time and are then edited live on the admin config
page (`/ui/admin`, system admins only), where a stored value wins over the env default.
Key variables:

| Variable | Purpose |
|----------|---------|
| `DATABASE_URL` | Postgres DSN (default local dev DSN). |
| `SERVER_ADDR` | HTTP listen address (default `:8080`). |
| `LOG_LEVEL` | Log verbosity (default `info`). |
| `EMBEDDING_PROVIDER` | Embedding backend: `ollama`, `gcp` (Vertex AI), `openai` (any OpenAI-compatible endpoint), `aws` (Bedrock), or `fake` (tests). |
| `EMBEDDING_DIMENSIONS` | Vector dimension; must match the model (default `768`). |
| `OLLAMA_URL`, `OLLAMA_MODEL` | Ollama endpoint and model. |
| `GCP_PROJECT`, `GCP_LOCATION`, `GCP_EMBEDDING_MODEL` | Vertex AI embedding config. |
| `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_EMBEDDING_MODEL` | OpenAI-compatible endpoint root, API key, and model. |
| `AWS_REGION`, `AWS_EMBEDDING_MODEL` | Bedrock region and embedding model. |
| `ADMIN_ALLOWED_EMAILS` | Bootstrap admin allowlist. |
| `CLEANUP_ENABLED`, `CLEANUP_INTERVAL_HOURS` | Enable and schedule the cleanup scanner (seed). |
| `MAX_REQUEST_BYTES`, `RATE_LIMIT_RPS`, `RATE_LIMIT_BURST` | Request-size and rate limits (seed). |
| `RATE_LIMIT_TRUSTED_PROXY_DEPTH` | Trusted reverse-proxy/CDN hop count for spoof-safe rate-limit client-IP keying; `0` (default) ignores `X-Forwarded-For` and keys on `RemoteAddr`. Behind a proxy/CDN it MUST be set to the real hop count, or every client shares one token bucket (their common source is the proxy's `RemoteAddr`). |
| `MEMORY_DEFAULT_OPTS` | Default per-tenant toggles (staleness mode, duplicate guard, cleanup scan). |
| `PUBLIC_BASE_URL` | Public origin of the deployment; used to derive OAuth issuer, audience, and redirect URIs. |
| `AUTHLET_MASTER_KEY` | 32-byte hex key that seals OAuth signing keys at rest. |
| `MEMORY_MCP_GOOGLE_CLIENT_ID`, `MEMORY_MCP_GOOGLE_CLIENT_SECRET` | Upstream OAuth client credentials. Setting **both** enables the OAuth/authlet path; setting neither leaves `/mcp` API-key-only. |
| `MEMORY_UI_CLIENT_ID` | Public PKCE client id for the web UI. |

Two globals live only in `instance_config` with **no env var** and are set on the admin
config page: the near-duplicate threshold (global default `0.85`, with a nullable
per-tenant override) and the cleanup `webhook_url` (empty disables the notifier).

The OAuth path is opt-in: it is enabled only when both Google client variables are set,
and config load then requires a valid `PUBLIC_BASE_URL` (an absolute `http`/`https`
origin with a host and no path). For example, a deployment reachable at
`https://mem.example.org` would set `PUBLIC_BASE_URL=https://mem.example.org`, yielding
an OAuth issuer of `https://mem.example.org` and a protected resource audience of
`https://mem.example.org/mcp`.

## Request pipeline

The HTTP handler composes middleware (innermost to outermost): CORS, request-size limit,
rate limiting, and security headers (a strict CSP applied outermost so error and
preflight responses carry it too). `/mcp` is wrapped by the auth-routing chain described
above. Health and readiness endpoints (`/~/health`, `/~/ready`, `/~/version`) are
unauthenticated, and a small static web UI is served under `/ui`.
