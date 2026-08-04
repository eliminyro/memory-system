# memory-system

**Persistent, semantic long-term memory for AI agents, served over the [Model Context Protocol](https://modelcontextprotocol.io).**

[![CI](https://img.shields.io/github/actions/workflow/status/eliminyro/memory-system/ci.yml?branch=master&label=CI)](https://github.com/eliminyro/memory-system/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/eliminyro/memory-system)](https://github.com/eliminyro/memory-system/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/eliminyro/memory-system)](go.mod)

---

## What it is

`memory-system` is a self-hostable MCP server that gives AI agents a durable, searchable
long-term memory across sessions. Documents are stored as markdown, split into sections,
embedded, and made retrievable through **hybrid search** — vector similarity fused with
lexical ranking — backed by PostgreSQL and the [`pgvector`](https://github.com/pgvector/pgvector)
extension. An agent stores what it learns as a self-contained note and recalls it later
by meaning, not just keyword.

It ships as a single Go binary (module `github.com/eliminyro/memory-system`) that serves
MCP over HTTP. On store, each section is embedded **before any database write**, so a
provider failure never leaves a half-written document; on search, vector candidates
ordered by cosine distance are fused with lexical candidates ranked by Postgres full-text
search. A per-`doc_type` staleness model, an optional near-duplicate guard, and a
retention sweep keep the corpus from rotting over time.

Every request passes through a Zanzibar-style, relationship-based **authorization
engine** — a single decision point backed by relation tuples rather than hard-coded
rules. Two independent authentication paths feed that engine: long-lived **API keys**
(ideal for headless agents and CI) and **OAuth 2.1 / OIDC** (for humans signing in
through an upstream identity provider). All data is tenant-scoped, with an optional
shared "common" pool for knowledge every tenant can read.

## Features

- **Hybrid retrieval** — pgvector HNSW cosine search fused with a GIN-indexed `tsvector`
  lexical search, scored as `1 - (embedding <=> query_vector)`.
- **Pluggable embedding providers** — `ollama`, `gcp` (Vertex AI), `openai` (any
  OpenAI-compatible `/embeddings` endpoint: OpenAI, Azure, vLLM, LM Studio, LocalAI,
  HuggingFace TEI), `aws` (Bedrock Titan / Cohere), and a deterministic `fake` provider
  for tests.
- **Dual authentication, one authz engine** — API keys *and* OAuth 2.1 / OIDC both
  resolve to a unified `Subject` and are checked by the same relationship engine.
- **Per-tenant isolation** — every document, key, and relation tuple is tenant-scoped,
  with a shared common pool for cross-tenant knowledge.
- **Staleness & verification** — per-`doc_type` freshness thresholds with `off`,
  `advisory`, or `hard` modes; stale sections can be withheld until re-verified.
- **Retention sweep** — archives documents left unverified past a configurable multiple
  of their staleness threshold, then hard-deletes after a grace window (guarded against
  destructive misconfiguration).
- **Duplicate guard** — an optional similarity check refuses near-duplicate writes,
  returning a `similar_exists` result so agents update instead of cloning.
- **Cleanup pipeline** — a nightly scanner queues near-duplicate candidates and can post
  a per-scan summary to Telegram.
- **Admin CLI + web UI** — a `memory-admin` binary that operates directly against the
  database (no server required, ideal for bootstrap), plus a small admin page served at
  `/ui`.
- **Self-service bootstrap & import** — a one-shot first-run setup gated by a token the
  server generates and logs at boot (no env var to manage), available over HTTP or CLI,
  plus an async, job-based document-archive import (admin UI, HTTP, or CLI), so a fresh
  instance needs no direct database access.
- **Rich MCP tool surface** — a regular tool set for agents and a privileged admin tool
  set, routed automatically by the caller's authorization.
- **Hardened HTTP edge** — request-size limits, token-bucket rate limiting, strict CSP
  and security headers, unauthenticated health/readiness endpoints.

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

Postgres stays on the internal compose network, but the server image also ships the
`memory-admin` binary at `/memory-admin`. Bootstrap the first tenant and API key by
exec-ing it inside the running container — it inherits the container's `DATABASE_URL`:

```bash
# 3. Create a tenant (note the returned id) and issue a key (plaintext shown ONCE)
docker compose exec memory-mcp /memory-admin tenant create --name quickstart
docker compose exec memory-mcp /memory-admin key issue --tenant <TENANT_ID> --label quickstart
# -> key: mmcp_XXXXXXXX...   (copy it now; it is never retrievable again)
```

> `memory-admin` is also a standalone binary (grab it from a release archive or
> `go build ./cmd/admin`) — point it at any reachable Postgres via `DATABASE_URL`.

Finally, generate an MCP client config and point a client at the server:

```bash
docker compose exec memory-mcp /memory-admin setup --url http://localhost:8090 --token mmcp_XXXXXXXX...
```

That emits a ready-to-paste `mcpServers` block. The server answers MCP at
`http://localhost:8090/mcp` with `Authorization: Bearer mmcp_...`; unauthenticated calls
receive a `401` JSON challenge. Health lives at `/~/health`.

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

| Variable | Default | Notes |
|----------|---------|-------|
| `DATABASE_URL` | `postgres://memory:memory@localhost:5432/memory?sslmode=disable` | PostgreSQL DSN (pgvector required). |
| `SERVER_ADDR` | `:8080` | HTTP listen address. |
| `LOG_LEVEL` | `info` | Log verbosity. |
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
| `ADMIN_ALLOWED_EMAILS` | *(unset)* | Bootstrap seed only — emails granted `system:memory#admin` at startup. |
| `MEMORY_RESET` | *(unset)* | Boot-only break-glass flag — clears the admin key(s) and re-arms bootstrap; never touches tenants/documents/memories. |
| `IMPORT_MAX_UPLOAD_BYTES` | `33554432` (32 MiB) | Cap on an uploaded import archive at `POST /api/admin/import`; exempt from `MAX_REQUEST_BYTES`. |
| `IMPORT_WORKER_CONCURRENCY` | `1` | Concurrent goroutines draining the async import job queue. |
| `MAX_REQUEST_BYTES` | `1048576` | Request-body cap; `0` disables. |
| `RATE_LIMIT_RPS` | `20` | Token-bucket rate over the auth/write surface; `<= 0` disables. |
| `RATE_LIMIT_BURST` | `40` | Bucket burst; must be `>= 1` when RPS `> 0`. |
| `RETENTION_MULTIPLIER` | `3` | Archive after this × the doc_type staleness threshold; must be `>= 1`. |
| `RETENTION_DELETE_GRACE_DAYS` | `30` | Hard-delete this many days after archiving; must be `>= 1`. |
| `CLEANUP_ENABLED` | `true` | Enable the nightly near-duplicate / retention scanner. |
| `CLEANUP_INTERVAL_HOURS` | `24` | Scan interval. |
| `TELEGRAM_BOT_TOKEN` | *(unset)* | Optional — post a per-scan cleanup summary. |
| `TELEGRAM_CHAT_ID` | *(unset)* | Optional — target chat for the summary. |
| `MEMORY_DEFAULT_OPTS` | *(unset)* | Per-tenant toggle defaults, e.g. `staleness=off,duplicate_guard=false,cleanup_scan_enabled=false`. |
| `AUTHLET_MASTER_KEY` | *(unset)* | 32-byte hex key encrypting OAuth signing material at rest. |
| `MEMORY_MCP_GOOGLE_CLIENT_ID` | *(unset)* | OAuth opt-in (set **with** the secret). |
| `MEMORY_MCP_GOOGLE_CLIENT_SECRET` | *(unset)* | OAuth opt-in (set **with** the client id). |
| `MEMORY_UI_CLIENT_ID` | *(unset)* | Public PKCE client id used by the web UI. |
| `PUBLIC_BASE_URL` | *(unset)* | External origin (e.g. `https://mem.example.org`). **Required** when the OAuth path is enabled. |

Setting **both** `MEMORY_MCP_GOOGLE_CLIENT_ID` and `MEMORY_MCP_GOOGLE_CLIENT_SECRET` opts
into the OAuth 2.1 / OIDC path; setting one without the other is a fatal boot error. When
the OAuth path is enabled, `PUBLIC_BASE_URL` must be an absolute `http(s)` origin with no
path. Leaving the Google client vars unset keeps `/mcp` API-key-only.

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

Two equivalent administrative surfaces exist:

- **`memory-admin` CLI** — operates directly against the database via `DATABASE_URL`, so
  it works even with no server running. This is the only way to bootstrap the first
  tenant and key. Holding `DATABASE_URL` is equivalent to full control — treat the CLI
  as an operator tool.
- **MCP admin tools** — the same operations exposed to an already-privileged MCP client
  (`list_tenants`, `create_tenant`, `update_tenant`, `delete_tenant`, `create_api_key`,
  `list_api_keys`, `revoke_api_key`, `rotate_api_key`, `grant_user`, `list_users`,
  `update_user_role`, `revoke_user`).

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
a dedicated setup page — paste the logged token plus an optional admin email — and
`POST /bootstrap` provisions the first tenant and admin, returning the plaintext admin
key exactly once; the whole `/bootstrap` surface **404s** once an admin exists. When
OAuth is configured, that optional admin email is granted `system:memory#admin` (the
same authority as the founding key), so the operator can sign in at `/ui` via OAuth with
full admin rights; the page shows the email field only when OAuth is wired up, and
otherwise notes that `/ui` stays unavailable until it is.
`memory-admin bootstrap` provisions the same way straight from the CLI with no token
needed (it talks to the database directly and is inherently privileged). An
operator-only `MEMORY_RESET=1` boot flag re-arms bootstrap by clearing the admin key(s)
— it never deletes tenant data, and the next boot logs a fresh token. Loading a corpus
works the same way: upload an archive from the admin UI, `POST /api/admin/import`
(multipart, a zip, processed asynchronously by a background worker), or run the
`memory-import` CLI over a local directory. See
[`docs/administering.md`](docs/administering.md) for the full first-run bootstrap,
break-glass reset, document import, user-management, and key-lifecycle runbook.

## Connecting a client

`memory-admin setup` emits the standard `mcpServers` JSON without writing your token to
disk:

```bash
memory-admin setup --url https://mem.example.org --token mmcp_your_token_here
# or: MEMORY_URL=... MEMORY_TOKEN=... memory-admin setup
```

Paste the emitted block into any MCP client that supports HTTP transport.

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

The server is one HTTP listener. `/mcp` carries the MCP Streamable-HTTP transport; a
`DualAuth` router inspects the bearer token (a three-segment JWT is verified by the OAuth
path, anything else falls through to API keys), writes a unified `Subject`, and the MCP
transport calls the authorization engine's `Check`. Application code lives under
`internal/` (config, database, repository, service, mcp, authz, auth, authletas,
middleware, cleanup, retention, staleness); entry points are `cmd/server`, `cmd/admin`,
and `cmd/import`. Data is stored in Postgres with a `vector(N)` embedding column per
section, an HNSW cosine index, a GIN lexical index, and a `relation_tuples` table for the
authorization graph. See [`docs/architecture.md`](docs/architecture.md) for the full
component map, data-flow walkthroughs, and the dual-auth model.

## Building from source

Requires Go (see the version in [`go.mod`](go.mod)).

```bash
go build ./...          # build all binaries (cmd/server, cmd/admin, cmd/import)
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
[`goreleaser`](.goreleaser.yaml) to produce the `memory-mcp`, `memory-import`, and
`memory-admin` binaries, archives, checksums, SBOMs, and a GitHub Release.

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
