# memory-system

**Persistent, semantic long-term memory for AI agents, served over the [Model Context Protocol](https://modelcontextprotocol.io).**

[![CI](https://img.shields.io/github/actions/workflow/status/eliminyro/memory-system/ci.yml?branch=master&label=CI)](https://github.com/eliminyro/memory-system/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/eliminyro/memory-system)](https://github.com/eliminyro/memory-system/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/eliminyro/memory-system)](https://goreportcard.com/report/github.com/eliminyro/memory-system)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/eliminyro/memory-system)](go.mod)

**[Quickstart](#quickstart) · [Configuration](#configuration) · [Architecture](#architecture) · [How it compares](#how-it-compares)**

---

## What it is

`memory-system` is a self-hostable MCP server that gives AI agents a durable, searchable
long-term memory across sessions. Documents are stored as markdown, split into sections,
embedded, and made retrievable through **hybrid search** — vector similarity fused with
lexical ranking — backed by PostgreSQL and the [`pgvector`](https://github.com/pgvector/pgvector)
extension. An agent stores what it learns as a self-contained note and recalls it later
by meaning, not just keyword.

**Who it's for:** individuals and teams self-hosting durable memory for their AI agents —
headless agents and CI authenticate with API keys; humans sign in over OAuth/OIDC.

![The memory-system web console — hybrid search over a tenant's memories, each tagged with its doc_type path and verification status.](docs/assets/ui-console.png)

It runs as a single Go binary serving MCP over HTTP. Each section is embedded **before any
database write**, so a provider failure never leaves a half-written document; on search,
vector candidates by cosine distance are fused with lexical candidates from Postgres
full-text search. A per-`doc_type` staleness model and an optional near-duplicate guard keep
the corpus healthy; retiring outdated knowledge is agent-driven (supersede, update, or
delete), not a timed sweep. Every request passes a Zanzibar-style, relationship-based
**authorization engine** — one decision point over relation tuples — fed by two auth paths:
long-lived **API keys** (headless agents, CI) and **OAuth 2.1 / OIDC** (humans via an
upstream IdP). Data is tenant-scoped with an optional shared "common" pool; a caller's
**reads aggregate** across their tenants, while **writes stay scoped to a single tenant**.

## Features

- **Hybrid retrieval** — pgvector HNSW cosine search fused with a GIN-indexed `tsvector`
  lexical search, scored as `1 - (embedding <=> query_vector)`.
- **Snippet mode** — opt-in `snippet: true` on `search_memory` returns a short
  match-centered window (`MEMORY_SNIPPET_CHARS`, default ~400) of each result instead of
  full content — cheap triage on search, then `get_document` for full text. A
  `snippet_centered` flag distinguishes a real lexical match from a leading-text fallback
  (purely-semantic hit). Verbatim (no LLM); withheld results are never snippet-expanded.
- **Pluggable embedding providers** — `ollama`, `gcp` (Vertex AI), `openai` (any
  OpenAI-compatible `/embeddings` endpoint: OpenAI, Azure, vLLM, LM Studio, LocalAI,
  HuggingFace TEI), `aws` (Bedrock Titan / Cohere), and a deterministic `fake` provider
  for tests.
- **Dual authentication, one authz engine** — API keys *and* OAuth 2.1 / OIDC both
  resolve to a unified `Subject` and are checked by the same relationship engine.
- **Per-tenant isolation** — every document, key, and relation tuple is tenant-scoped,
  with a shared common pool for cross-tenant knowledge.
- **Aggregated reads across your tenants** — `search_memory`, `list_documents`, and
  `get` span the caller's readable tenant set (home tenant + common pool + any tenant
  they hold a direct `viewer`/`member`/`manager`/`admin` grant on), with each result
  labeled by its owning tenant and an optional `tenant_id` filter to narrow to one.
  Admins are not expanded to every tenant, and writes stay single-tenant.
- **Staleness & verification** — per-`doc_type` freshness thresholds as a recall-time
  *signal*: `off` (none), `advisory` (warn in the response), or `hard` (withhold stale
  content until re-verified). The signal never deletes.
- **Agent-driven retirement** — no automated time-based sweep archives or deletes aged
  documents. Agents retire knowledge deliberately: supersede an entry (a `supersedes`
  edge archives its target and purges its content into a lineage-only tombstone; the
  content is not recoverable), update it in place, or delete it.
- **Live global configuration** — an admin-only config page at `/ui/admin` (system admins
  only) edits runtime globals in the database with no restart: retrieval tuning, rate
  limits, new-tenant defaults, the near-duplicate threshold, and the cleanup webhook.
  Environment variables seed the initial values on first migrate; a stored value then wins.
- **Duplicate guard** — an optional similarity check refuses near-duplicate writes,
  returning a `similar_exists` result so agents update instead of cloning. The threshold is
  a global default (`0.85`) with an optional per-tenant override.
- **Cleanup pipeline** — a nightly scanner queues near-duplicate candidates for review and
  can POST a per-scan JSON summary to a configurable webhook, plus a companion sweep that
  hard-deletes long-dead (revoked or expired) API keys so key listings stay tidy.
- **Web console + MCP admin tools** — a full web console at `/ui`: browse and edit
  memories (color-coded per owning tenant), manage tenants (create, rename, delete),
  members and per-document ACL grants, and API keys (create, rotate, revoke, purge), and
  run archive imports. The same tenant, key, and user operations are also exposed as a
  privileged MCP admin tool set. Every surface is gated by the caller's authorization.
- **Self-service bootstrap & import** — a one-shot first-run setup gated by a token the
  server generates and logs at boot (no env var to manage), driven over HTTP at
  `/bootstrap`, plus an async, job-based document-archive import (admin UI or HTTP), so a
  fresh instance needs no direct database access.
- **Rich MCP tool surface** — a regular tool set for agents and a privileged admin tool
  set, routed automatically by the caller's authorization.
- **Hardened HTTP edge** — request-size limits, token-bucket rate limiting, strict CSP
  and security headers, unauthenticated health/readiness endpoints.

## How it compares

Persistent memory for agents is a small, active category. The closest projects are
[`edg-l/engram-mcp`](https://github.com/edg-l/engram-mcp) (Rust, single-user, local) and
[`softmaxdata/engram`](https://github.com/softmaxdata/engram) (Python, LLM-driven). They
solve a related problem with different priorities — this table is about fit, not ranking.

| | **memory-system** | engram-mcp | softmaxdata/engram |
|---|---|---|---|
| Stack | Go · Postgres + pgvector | Rust · SQLite + FTS5 | Python · SQLite/Postgres |
| Multi-tenant | Isolation + shared common pool | Single-user, single-node | Single-store |
| Auth | API keys **+** OAuth 2.1/OIDC, Zanzibar authz | None (*"local MCP, no auth"*) | None documented |
| Write path | Deterministic, **verbatim — no LLM** | Deterministic (local ONNX) | **LLM required** (Reflector/Curator rewrite) |
| Embedders | **5 pluggable** (ollama · Vertex · OpenAI-compat · Bedrock · fake) | 1 (quantized 256-dim ONNX) | Provider LLM embeddings |
| Fusion | RRF hybrid + tunable MMR | RRF hybrid | vector + MMR |
| Reach | Server — any MCP client, any machine | Local only (export/import) | Server |
| Retrieval benchmark | Published (LongMemEval-S) | Published (LongMemEval-S) | — |

engram-mcp's stated non-goals — *no auth, not multi-tenant, no cross-PC sync* — are
memory-system's core thesis: a governed, multi-tenant server with a shared pool and a
single authorization decision point. And unlike an LLM-rewrite store, memory-system keeps a
**faithful-custody** contract: it stores and returns *your* words, and every governance
decision (who may read, is it stale, is it a duplicate) is deterministic and auditable — no
model paraphrases your notes on the way in.

## Retrieval quality

memory-system ships a **reproducible retrieval benchmark** on
[LongMemEval-S](https://github.com/xiaowu0162/LongMemEval) (Wu et al., ICLR 2025) — a
long-term-memory needle-in-a-haystack set — driving the real search path and reporting
session-level recall\@k / MRR. Headline (Vertex `text-embedding-005`, 768-dim; seed 42,
N=150):

| mode | partial-R\@5 | full-R\@5 | MRR\@5 |
|---|---:|---:|---:|
| hybrid (RRF) | 98.7% | 88.7% | 0.954 |
| vector-only | 98.7% | 88.7% | 0.954 |
| lexical-only | 8.7% | 3.3% | 0.087 |
| **hybrid + MMR (λ=0.5)** | **99.3%** | **91.3%** | **0.956** |

What it shows, honestly:
- **The embedder carries the signal on this workload** — hybrid ≡ vector to the digit. RRF
  fusion here is for score-*correctness* and robustness, not extra recall.
- **Lexical-only collapses** (3.3% full-R\@5) — paraphrased questions defeat keyword match.
  That is a LongMemEval property, not a verdict on lexical for real notes/IDs/code that
  share vocabulary; the arm is kept, not de-weighted, on this evidence alone.
- **MMR diversity re-rank helps the hard cases.** Tuned to `λ=0.5` (the shipped default), it
  lifts full-recall on multi-evidence questions — full-R\@5 88.7% → **91.3%** — where a
  single evidence session isn't enough.
- **The asymmetric part:** memory-system ships five embedder backends, so it can publish
  *retrieval quality vs. embedder* — the axis a single-embedder project structurally can't.
  The table above is the Vertex flagship row; ollama / OpenAI rows will follow.

Full per-mode, per-question-type tables and the MMR λ sweep live in
[`benchmarks/longmemeval/RESULTS.md`](benchmarks/longmemeval/RESULTS.md); reproduce with the
harness in that directory.

## Quickstart

The bundled [`docker-compose.yml`](docker-compose.yml) brings up the full stack — the
memory server, PostgreSQL (`pgvector/pgvector:pg17`), and Ollama for embeddings — on the
API-key path. The server is published on host port **8090** (mapped to the container's
`8080`).

```bash
# 1. Start Postgres, Ollama, and the memory server (published on http://localhost:8090)
docker compose up -d

# 2. Load the embedding model into Ollama (matches OLLAMA_MODEL=nomic-embed-text, 768-dim)
docker compose exec ollama ollama pull nomic-embed-text
```

On first boot, while no admin exists, the server logs a one-time bootstrap token at
`WARN`. Read it from the container logs, then provision the first tenant and admin key
through the setup page:

```bash
# 3. Read the one-time bootstrap token the server logged at startup
docker compose logs memory-mcp | grep bootstrap_token | tail -1
```

The token is regenerated on every boot until the first admin exists, so use the most recent
log line. Open `http://localhost:8090/bootstrap`, paste the token, and submit (or
`POST /bootstrap` with the token to script it). The page provisions the first tenant and
admin key and shows the plaintext key **once** — `mmcp_XXXXXXXX...`; copy it now, it is never
retrievable again. The `/bootstrap` surface 404s once an admin exists.

> Stuck on a first-run error? See [`docs/troubleshooting.md`](docs/troubleshooting.md).

Point a client at the server with an `mcpServers` block:

```json
{
  "mcpServers": {
    "memory": {
      "type": "http",
      "url": "http://localhost:8090/mcp",
      "headers": { "Authorization": "Bearer mmcp_XXXXXXXX..." }
    }
  }
}
```

The server answers MCP at `http://localhost:8090/mcp` with `Authorization: Bearer mmcp_...`;
unauthenticated calls receive a `401` JSON challenge. Health lives at `/~/health`. When the
OAuth path is configured, the `/ui` "Connect an MCP client" panel generates the same block
for you.

## Container image

Prebuilt multi-arch images (`linux/amd64`, `linux/arm64`) are published on every release
to **GHCR** (canonical) and mirrored to **Docker Hub** — pull from whichever you prefer:

```bash
docker pull ghcr.io/eliminyro/memory-system:latest   # GHCR — no pull rate limits
docker pull eliminyro/memory-system:latest           # Docker Hub mirror
```

Tags are `:vX.Y.Z` (semver), `:latest`, and `:<git-sha>`. The `docker-compose.yml` above
builds from source for a hackable dev loop; to run a pinned release instead, swap
`build: .` for `image: ghcr.io/eliminyro/memory-system:latest`.

## Configuration

All configuration is via environment variables (parsed by
[`caarlos0/env`](https://github.com/caarlos0/env); see
[`internal/config/config.go`](internal/config/config.go)). Defaults are chosen so a stock
Ollama deploy works out of the box.

Two kinds of variable appear below. Most are **boot-only** — read once at startup and
fixed until a restart (`DATABASE_URL`, the embedding provider/model/dimension, the
OAuth/authlet vars, `PUBLIC_BASE_URL`, `MEMORY_RESET`, and the
import bounds). The rest, tagged **seed**, are *bootstrap defaults*: they seed the DB
(`instance_config`) on first migrate, after which the live value is read from the database
and edited without a restart on the admin config page (`/ui/admin`, system admins only). A
stored value always wins over the env default — see
[Live global configuration](#live-global-configuration).

| Variable | Default | Notes |
|----------|---------|-------|
| `DATABASE_URL` | `postgres://memory:memory@localhost:5432/memory?sslmode=disable` | PostgreSQL DSN (pgvector required). |
| `SERVER_ADDR` | `:8080` | HTTP listen address. |
| `LOG_LEVEL` | `info` | **seed** — log verbosity. |
| `EMBEDDING_PROVIDER` | `ollama` | `ollama` \| `gcp` \| `openai` \| `aws` \| `fake`. |
| `EMBEDDING_DIMENSIONS` | `768` | **Must** equal the dimension your model emits (startup guard enforces it). |
| `OLLAMA_URL` | `http://localhost:11434` | Ollama server URL. |
| `OLLAMA_MODEL` | `nomic-embed-text` | Ollama embedding model. |
| `GCP_PROJECT` | *(unset)* | Required when `EMBEDDING_PROVIDER=gcp`. |
| `GCP_LOCATION` | `us-central1` | Vertex AI region. |
| `GCP_EMBEDDING_MODEL` | `text-embedding-005` | Vertex AI embedding model. |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | API root exposing `/embeddings`. |
| `OPENAI_API_KEY` | *(unset)* | Optional; sent as a bearer token when set. |
| `OPENAI_EMBEDDING_MODEL` | *(unset)* | **Required** when `EMBEDDING_PROVIDER=openai`. |
| `AWS_REGION` | *(unset)* | **Required** when `EMBEDDING_PROVIDER=aws`. |
| `AWS_EMBEDDING_MODEL` | *(unset)* | **Required** when `EMBEDDING_PROVIDER=aws`. |
| `ADMIN_ALLOWED_EMAILS` | *(unset)* | **seed** — emails granted `system:memory#admin` at startup; editable on the config page (applied on restart). |
| `MEMORY_RESET` | *(unset)* | Boot-only break-glass flag — clears the admin key(s) and re-arms bootstrap; never touches tenants/documents/memories. |
| `IMPORT_MAX_UPLOAD_BYTES` | `33554432` (32 MiB) | Cap on an uploaded import archive at `POST /api/admin/import`; exempt from `MAX_REQUEST_BYTES`. |
| `IMPORT_WORKER_CONCURRENCY` | `1` | Concurrent goroutines draining the async import job queue. |
| `MAX_REQUEST_BYTES` | `1048576` | **seed** — request-body cap; `0` disables. |
| `RATE_LIMIT_RPS` | `20` | **seed** — token-bucket rate over the auth/write surface; `<= 0` disables. |
| `RATE_LIMIT_BURST` | `40` | **seed** — bucket burst; must be `>= 1` when RPS `> 0`. |
| `RATE_LIMIT_TRUSTED_PROXY_DEPTH` | `0` | **seed** — number of trusted reverse-proxy/CDN hops in front for client-IP rate-limit keying. `0` (default) trusts none: `X-Forwarded-For` is ignored and the key is `RemoteAddr` (unspoofable). Behind a proxy/CDN, set it to your trusted-hop count so the client IP is read from the right `X-Forwarded-For` entry; leaving it `0` there makes all traffic share one bucket. |
| `CLEANUP_ENABLED` | `true` | **seed** — enable the nightly near-duplicate cleanup scanner. |
| `CLEANUP_INTERVAL_HOURS` | `24` | **seed** — scan interval (shared by the dead-key sweep); a change takes effect at the next scheduler fire. |
| `REQUIRE_CONFIG_LISTENER` | `false` | **seed** — when `true`, a dead config-invalidation listener fails `/~/ready` (never `/~/health`). Off by default: a single replica gets every change write-through and has no peers to fall behind. Turn it on for multi-replica deployments. See [Architecture](#architecture). |
| `MEMORY_MMR_LAMBDA` | `0.5` | **seed** — MMR diversity re-rank lambda for hybrid search; range `(0, 1]`. `0.5` is the LongMemEval-tuned optimum; `1.0` disables (pure relevance). |
| `MEMORY_CANDIDATE_POOL` | `20` | **seed** — per-list SQL `LIMIT` each of the semantic and lexical candidate lists draws before fusion; range `[1, 1000]`. |
| `MEMORY_SNIPPET_CHARS` | `400` | **seed** — match-centered window size (chars) returned when `search_memory` is called with `snippet: true`; must be `> 0`. Approximate on the low end (`ts_headline` windows by words). |
| `MEMORY_STALENESS_PENALTY` | `0.2` | **seed** — down-weights stale docs in hybrid ranking; range `[0, 1]` (`0` = off). A doc verified past its `doc_type` staleness threshold ranks lower, down to `1-penalty` at 2× the threshold. Re-ranks only; never deletes. |
| `MEMORY_HISTORY_RETENTION_DAYS` | `90` | **seed** — mutation-history prune horizon (only matters when the history toggle is on); `>= 0`, where `0` = never prune (keep full history). |
| `MEMORY_DEFAULT_OPTS` | *(safe bundle)* | **seed** — per-tenant toggle defaults, applied at **tenant-create time only** (existing tenants keep their settings). The built-in default is the safe bundle `staleness=hard,duplicate_guard=true,cleanup_scan_enabled=true`; override to loosen, e.g. `staleness=off,duplicate_guard=false,cleanup_scan_enabled=false`. See [`docs/administering.md`](docs/administering.md#defaults-for-new-tenants). |
| `MEMORY_SELF_SERVICE_POLICY` | `open` | **seed** — global default for the self-service gate (`open` \| `admin_only`) over per-tenant settings editing and API-key creation. `open` lets a tenant's manager/owner self-serve; `admin_only` raises both to system-admin. A per-tenant override (set by an admin) takes precedence. |
| `AUTHLET_MASTER_KEY` | *(unset)* | 32-byte hex key encrypting OAuth signing material at rest. |
| `MEMORY_MCP_GOOGLE_CLIENT_ID` | *(unset)* | OAuth opt-in (set **with** the secret). |
| `MEMORY_MCP_GOOGLE_CLIENT_SECRET` | *(unset)* | OAuth opt-in (set **with** the client id). |
| `MEMORY_UI_CLIENT_ID` | *(unset)* | Public PKCE client id used by the web UI. |
| `PUBLIC_BASE_URL` | *(unset)* | External origin (e.g. `https://mem.example.org`). **Required** when the OAuth path is enabled. |
| `SIGNUP_ALLOWED_DOMAINS` | *(unset)* | **seed** — self-serve signup gate (OAuth path). **Empty/unset means PUBLIC signup** — any verified identity your IdP authenticates can self-provision a tenant. Set a comma-separated domain allow-list (e.g. `example.com,corp.example.com`) to lock signup to your org. Fails closed (403); never affects already-provisioned users. |

Setting **both** `MEMORY_MCP_GOOGLE_CLIENT_ID` and `MEMORY_MCP_GOOGLE_CLIENT_SECRET` opts
into the OAuth 2.1 / OIDC path; setting one without the other is a fatal boot error. When
the OAuth path is enabled, `PUBLIC_BASE_URL` must be an absolute `http(s)` origin with no
path. Leaving the Google client vars unset keeps `/mcp` API-key-only.

> **Warning — signup is PUBLIC by default.** With the OAuth path enabled, the first
> verified login from an identity that has no existing membership **auto-provisions a
> personal tenant** for that user (they become its owner). Identity is anchored on the
> OIDC `(issuer, subject)` pair — email is a mutable attribute, not the key, so a later
> email change still resolves to the same tenant. If `SIGNUP_ALLOWED_DOMAINS`
> is unset or empty, this is **public** — anyone your IdP will authenticate (for Google,
> *any* Google account) can self-provision. To lock a private instance to your
> organization, set `SIGNUP_ALLOWED_DOMAINS` to your domain(s). The gate does not affect
> already-provisioned users, and the first admin is separately gated by the bootstrap
> token. See
> [`docs/administering.md`](docs/administering.md#self-serve-signup-and-the-domain-gate).

### Live global configuration

The **seed** variables above are only *bootstrap* defaults. On first migrate each seeds
its column in the `instance_config` singleton; thereafter the live value is read from the
database and edited — without a restart — on the admin config page at **`/ui/admin`**
(system admins only). A stored value always wins over the env default. Most changes apply
immediately (retrieval tuning, toggles, new-tenant defaults, self-service, log level, rate
limits, request-size cap); `CLEANUP_INTERVAL_HOURS` takes effect at the next scheduler fire.

Two globals have **no env var** and are set only on the page:

- **Near-duplicate threshold** — the global default cosine cutoff for the duplicate guard
  (`0.85`), with an optional per-tenant override (unset ⇒ inherit the global default).
- **Cleanup webhook URL** — the near-duplicate cleanup scan POSTs a JSON summary here on
  each run; empty disables it. This replaces the previous Telegram notifier.

`ADMIN_ALLOWED_EMAILS` seeds the config page's admin-emails field: it grants
`system:memory#admin` at startup, so edits there apply on the next restart. Live
grant/revoke is in the Tenants admin UI.

### Embedding providers

| `EMBEDDING_PROVIDER` | Env vars | Example model | Typical dim | Credentials |
|----------------------|----------|---------------|-------------|-------------|
| `ollama` | `OLLAMA_URL`, `OLLAMA_MODEL` | `nomic-embed-text` | 768 | none |
| `gcp` | `GCP_PROJECT`, `GCP_LOCATION`, `GCP_EMBEDDING_MODEL` | `text-embedding-005` | 768 | Application Default Credentials |
| `openai` | `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | 1536 (configurable) | `OPENAI_API_KEY` (optional for local servers) |
| `aws` | `AWS_REGION`, `AWS_EMBEDDING_MODEL` | `amazon.titan-embed-text-v2:0` | 1024 | standard AWS credential chain |
| `fake` | — | — | `EMBEDDING_DIMENSIONS` | none (deterministic; tests only) |

`EMBEDDING_DIMENSIONS` must match what the model emits — a startup guard freezes the
embedding identity (provider + model + dimension) and refuses to run a populated corpus
whose stored dimension differs. See
[`docs/embedding-providers.md`](docs/embedding-providers.md) for per-provider detail,
including Azure/vLLM/TEI base URLs and output-dimension truncation.

## Administration

Two administrative surfaces exist:

- **Web console (`/ui`)** — browse and edit memories, manage tenants, members and
  per-document ACL grants, API keys, and archive imports, all gated by the signed-in
  admin's authorization.
- **MCP admin tools** — the same operations exposed to an already-privileged MCP client
  (`list_tenants`, `create_tenant`, `update_tenant`, `delete_tenant`, `create_api_key`,
  `list_api_keys`, `revoke_api_key`, `rotate_api_key`, `grant_user`, `list_users`,
  `update_user_role`, `revoke_user`, plus `grant_tenant_access`, `revoke_tenant_access`,
  and `list_tenant_grants`).

The first admin is provisioned over HTTP at `/bootstrap` (below).

Admin status is pure relationship state: a principal is an admin **iff** it holds the
global grant `system:memory#admin`. `ADMIN_ALLOWED_EMAILS` only seeds that grant at
startup. Keys are issued as opaque `mmcp_...` tokens, stored only as a SHA-256 hash, and
shown in plaintext exactly once. Rotation issues a replacement and can hold the
predecessor valid for a grace window for zero-downtime swaps. A small admin web page is
served at `/ui`, gated on bootstrap state: **404** before an admin exists, a static
"OAuth not configured" notice once bootstrapped without OAuth wired up, and the real
knowledge browser once both hold — it never shows a login or setup form itself.

A fresh instance can bootstrap itself instead of requiring direct database access: on
first boot, while no admin exists yet, the server generates a one-time bootstrap token
and logs it at `WARN` (read it with `docker logs`; it is regenerated every boot until an
admin exists and is never persisted or logged again afterward). `GET /bootstrap` serves
a dedicated setup page: paste the logged token and pick a mode. The page always shows
OAuth status and offers two modes — **MCP tokens** (provision the admin Bearer key) and
**OAuth** (additionally seed a founding admin email). `POST /bootstrap` provisions the
first tenant and admin, returning the plaintext admin key exactly once (always, in
either mode); the whole `/bootstrap` surface **404s** once an admin exists. When OAuth
is configured, the optional founding admin email is granted `system:memory#admin` (the
same authority as the founding key), so the operator signs in at `/ui` via OAuth with
full admin rights. With OAuth enabled the server also **auto-registers the `/ui` OAuth
client on boot** (from `MEMORY_UI_CLIENT_ID`, defaulting to `memory-ui`, and
`PUBLIC_BASE_URL`), so browser login works with no manual database access. (Configuring
the OAuth provider from the page itself, persisted to the DB, is planned.)
An operator-only `MEMORY_RESET=1` boot flag re-arms bootstrap by clearing the admin key(s)
— it never deletes tenant data, and the next boot logs a fresh token. Loading a corpus
works the same way: upload an archive from the admin UI or `POST /api/admin/import`
(multipart, a zip, processed asynchronously by a background worker). See
[`docs/administering.md`](docs/administering.md) for the full first-run bootstrap,
break-glass reset, document import, user-management, and key-lifecycle runbook.

## Connecting a client

Point any MCP client that supports the HTTP transport at the server with an `mcpServers`
block:

```json
{
  "mcpServers": {
    "memory": {
      "type": "http",
      "url": "https://mem.example.org/mcp",
      "headers": { "Authorization": "Bearer mmcp_your_token_here" }
    }
  }
}
```

The `/ui` "Connect an MCP client" panel generates this block for you (with a placeholder in
place of the real key).

When OAuth is configured, a static key is optional: an MCP client that supports OAuth can
connect with **just the server `/mcp` URL** — it discovers the authorization server from
the protected-resource metadata, registers dynamically, and completes the PKCE login
flow, so there is no token to mint or store. Use a static `mmcp_...` key as a Bearer
token for clients (or CI) that don't do OAuth. The `/ui` "Connect an MCP client" panel
shows both paths. See
[`docs/connecting-clients.md`](docs/connecting-clients.md) for client examples, and
[`docs/memory-usage-prompt.md`](docs/memory-usage-prompt.md) for a persona-free,
ready-to-paste system-prompt block that teaches an assistant to recall before acting,
store durable learnings, and update instead of duplicating.

## Architecture

```mermaid
flowchart LR
  agent["AI agent / MCP client"] -->|"API key"| mcp["/mcp · MCP over HTTP"]
  human["Human"] -->|"OAuth 2.1 / OIDC"| ui["/ui console"]
  ui --> mcp
  mcp --> auth["DualAuth → unified Subject"]
  auth --> check["Authorization engine · Check()"]
  check --> svc["service + repository"]
  svc --> pg[("PostgreSQL + pgvector<br/>vector(N) · HNSW · GIN · relation_tuples")]
```

The server is one HTTP listener. `/mcp` carries the MCP Streamable-HTTP transport; a
`DualAuth` router inspects the bearer token (a three-segment JWT is verified by the OAuth
path, anything else falls through to API keys), writes a unified `Subject`, and the MCP
transport calls the authorization engine's `Check`. Application code lives under
`internal/` (config, database, repository, service, mcp, authz, auth, authletas,
middleware, cleanup, staleness); the entry point is `cmd/server`. Data is
stored in Postgres with a `vector(N)` embedding column per
section, an HNSW cosine index, a GIN lexical index, and a `relation_tuples` table for the
authorization graph. See [`docs/architecture.md`](docs/architecture.md) for the full
component map, data-flow walkthroughs, and the dual-auth model.

### Config invalidation across replicas

Cached configuration is refreshed write-through: the replica that writes updates its own
in-memory snapshot immediately. To converge the *other* replicas, an `AFTER` trigger on
each config table calls `pg_notify` at commit — `instance_config` signals on the
`instance_config_changed` channel — and one shared listener (`internal/pgnotify`) holds a
dedicated connection, `LISTEN`ing and reloading the affected cache. The notification is a
signal, not a payload: the reload re-reads the table, so it can never deliver a partial or
stale view. Each cached surface registers a `(channel, reload)` pair; the listener owns the
connection, dispatch, reconnect, and health, and knows nothing about what any cache holds.

Two guarantees the design turns on:

- **`LISTEN` precedes the initial load.** The listener establishes its subscriptions before
  any cache loads, so a change committed during startup is never missed.
- **Reconnect reloads unconditionally.** Postgres queues no notifications for a disconnected
  client, so on every reconnect the listener reloads every registered cache rather than
  guessing what it missed.

Write-through is kept, so a dead listener never delays the replica making the change — it
only leaves *other* replicas stale until they reconnect. That staleness is surfaced on
`/~/ready` when `REQUIRE_CONFIG_LISTENER` is on (never on `/~/health`); it defaults off
because a single replica has no peers to fall behind.

## Building from source

Requires Go (see the version in [`go.mod`](go.mod)).

```bash
go build ./...          # build the server binary (cmd/server)
go test ./...           # unit tests
```

Integration tests are behind a build tag and need a live PostgreSQL (with `pgvector`).
Point `TEST_DATABASE_URL` at it and serialize packages so parallel migrations don't race:

```bash
export TEST_DATABASE_URL='postgres://memory:memory@localhost:5432/memory?sslmode=disable'
go test -tags=integration -p 1 ./...
```

Releases are automated: on a green CI run on `master`, the release workflow computes the
next version, tags it, builds and pushes a multi-arch image to `ghcr.io/eliminyro/memory-system`
(mirrored to Docker Hub when its credentials are configured), and runs
[`goreleaser`](.goreleaser.yaml) to produce the `memory-mcp` binary, archives, checksums,
SBOMs, and a GitHub Release.

## Status and limits

Honest about what it is and isn't:

- **Single-node Postgres.** Durability, backups, and availability are Postgres's to provide;
  there is no built-in sharding or HA — front it with a Postgres HA setup if you need one.
  See [backup, restore & upgrade](docs/administering.md#backup-restore-and-upgrade) and the
  [production checklist](docs/administering.md#running-in-production).
- **Embedder identity is fixed to the vector column.** Changing the provider, model, or
  dimension means re-embedding the whole corpus; the server refuses a silent mismatch rather
  than corrupting search.
- **Pick the embedder first.** The benchmark shows the embedder is the dominant lever on
  recall — RRF and MMR are measured refinements on top, not substitutes for a good embedder.
- **No LLM in the write path, by design.** The server stores your words verbatim and never
  summarizes, paraphrases, or "corrects" them. That is a deliberate faithful-custody
  contract — a different product from memory systems that rewrite your notes on ingest.
- **History is an audit trail, not rollback.** Mutation history is append-only, shared-tenant,
  and toggle-gated; `update_section` overwrites — there is no per-edit version restore.
- **Opt-in by default.** Mutation history is off until an operator enables its global
  toggle; the duplicate guard and staleness signal are per-tenant opt-in.

## References

- **RRF (hybrid fusion)** — Cormack, Clarke & Büttcher, *Reciprocal Rank Fusion Outperforms
  Condorcet and Individual Rank Learning Methods*, SIGIR 2009.
- **MMR (diversity re-rank)** — Carbonell & Goldstein, *The Use of MMR, Diversity-Based
  Reranking for Reordering Documents and Producing Summaries*, SIGIR 1998.
- **LongMemEval (benchmark)** — Wu et al., *LongMemEval: Benchmarking Chat Assistants on
  Long-Term Interactive Memory*, ICLR 2025 · [dataset](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned).
- **Foundations** — [pgvector](https://github.com/pgvector/pgvector) ·
  [Model Context Protocol](https://modelcontextprotocol.io).

## Contributing

Contributions are welcome.

- Use [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`,
  `refactor:`, `chore:`, …) — release versioning is derived from commit history.
- Code must be `gofmt`-clean and pass `golangci-lint run`.
- Keep tests green: `go test ./...` and, where relevant, the integration suite above. CI
  runs gofmt, golangci-lint, unit tests, integration tests, a build, and a boot smoke
  test on every pull request.

## License

Licensed under the **GNU Affero General Public License v3.0**. See [`LICENSE`](LICENSE).

Note the AGPL's §13 network-use clause: if you run a modified version of this software and
let others interact with it over a network, you must offer those users the corresponding
source of your modified version. Hosting a fork is fine — publishing its source is the
condition.
