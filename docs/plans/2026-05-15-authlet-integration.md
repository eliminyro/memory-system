# Authlet Integration for Memory MCP

**Date:** 2026-05-15
**Spec:** `~/mystuff/docs/superpowers/specs/2026-05-13-authlet-design.md` (note: federated topology in spec is superseded — see below)
**Status:** Planning
**Prereq:** Hilo authlet AS is live in prod, used as the reference implementation pattern.

## Goal

Make `https://memory-mcp.a11s.dev/mcp` accessible from Claude.ai as a custom MCP connector via OAuth 2.1 + DCR, while keeping the existing API-key auth path (DualAuth) so `memory-agent` CLI continues to work unchanged.

## Identity Topology

**Memory MCP and Hilo are independent peers. No federation between them.** Both expose their own authlet AS to Claude; both use Google directly as upstream IdP.

```
Claude.ai (DCR)                  Claude.ai (DCR)
    │                                │
    ▼                                ▼
Hilo authlet AS                Memory MCP authlet AS
(https://hilo.eliminyro.me)    (https://memory-mcp.a11s.dev)
    │                                │
    ▼                                ▼
   Google                          Google
```

Each AS issues its own JWT access tokens scoped to its own audience. Memory MCP knows nothing about Hilo and vice versa.

## Constants for Memory MCP authlet

| Field | Value |
|---|---|
| Issuer | `https://memory-mcp.a11s.dev` |
| PathPrefix | `/oauth` |
| Audience | `https://memory-mcp.a11s.dev/mcp` |
| PRM URL | `https://memory-mcp.a11s.dev/.well-known/oauth-protected-resource/mcp` |
| Upstream Issuer | `https://accounts.google.com` |
| Redirect URI | `https://memory-mcp.a11s.dev/oauth/idp/callback` |
| Scope | `mcp` |

## Phase A — GORM `authletstore` adapter

Port `~/mystuff/goprojects/hilo/internal/authletstore/` → `~/mystuff/goprojects/memory-system/internal/authletstore/`.

Files:
- `models.go` — `OAuthClient`, `OAuthCode`, `OAuthRefreshToken`, `FamilyRevocation`, `AuthletSigningKey`
- `store.go` — `Store` aggregating the four sub-stores
- `clients.go`, `codes.go`, `refresh.go`, `signing_keys.go`
- `string_array.go` — `StringArray` (Postgres text[] ↔ Go []string)
- `*_test.go` — port hilo's tests verbatim

Migration: new SQL file in `internal/database/migrations/` creating all five tables. Idempotent (`CREATE TABLE IF NOT EXISTS`).

**No behavior change yet** — tables exist, store implements interface, nothing calls it.

**Branch:** `authlet-storage`. **PR:** "feat: GORM-backed authletstore adapter + tables".

## Phase B — `tenant_users` + `MemoryUserResolver`

New model: `internal/models/tenant_user.go`.

```go
type TenantUser struct {
    ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
    Email     string    `gorm:"size:320;not null;uniqueIndex"` // one email → one tenant
    TenantID  uuid.UUID `gorm:"type:uuid;not null;index"`
    Role      string    `gorm:"size:16;not null;default:'member'"` // member | admin
    CreatedAt time.Time
    Tenant    *Tenant   `gorm:"foreignKey:TenantID;constraint:OnDelete:CASCADE"`
}
func (TenantUser) TableName() string { return "tenant_users" }
```

Schema invariant: `email` is globally unique — a single email maps to exactly one tenant. Resolver does `WHERE email = ? LIMIT 1` and returns deterministically.

`MemoryUserResolver` at `internal/authletas/resolver.go`:
- Input: `idp.Claims` from Google (claims.Email is the trust anchor; claims.EmailVerified must be true)
- Lookup: `tenant_users WHERE email = ? LIMIT 1` (single-tenant per email for now)
- Output: `tenant_id` string (used as authlet `sub` claim → JWT `sub`)
- Error: `ErrUnauthorized` if no row matches or email unverified

Tests: known verified email → tenant_id; unknown email → unauthorized; unverified email → unauthorized; empty email → unauthorized.

`idTokenClaims` callback for authlet's AS: joins `tenant_users` and returns the email passed in. Name is not stored locally — Memory MCP has no `users` table, only `tenant_users`. ID tokens will carry `email` only.

**Branch:** `tenant-users`. **PR:** "feat: tenant_users mapping + user resolver".

## Phase C — `internal/authletas` wiring package

Port hilo's `internal/authletas/` files: `setup.go`, `dualauth.go`, `wwwauth.go`, `user_bridge.go`.

Adjustments from hilo's version:
- `Issuer = "https://memory-mcp.a11s.dev"` (config-driven so dev/prod differ)
- `Audience = Issuer + "/mcp"` (no `/api/` prefix — Traefik on memory-mcp doesn't strip)
- `upstreamIssuer = "https://accounts.google.com"` (same as hilo — independent Google client)
- `redirectURI = Issuer + "/oauth/idp/callback"`
- `Setup` signature takes `googleClientID`, `googleClientSecret` (Memory MCP's own Google OAuth client, separate from hilo's)
- `idTokenClaims` reads from `tenant_users` not `users`; name is omitted (not stored)
- `UserResolver` is `MemoryUserResolver`, not `HiloUserResolver`
- `TrySetup` wrapper identical to hilo's (graceful boot on missing config)

The chi-mux assumption stays — we'll handle stdlib-mux conversion in Phase D.

**Branch:** `authletas-wiring`. **PR:** "feat: authletas wiring for memory-mcp".

## Phase D — Mount in `cmd/server/main.go`

Memory MCP uses `http.NewServeMux()` (stdlib), not chi. Hilo's `Wiring.Mount(r chi.Router)` won't compile here.

Options for the mount adapter (pick one, document in plan amendment):
1. **Add chi as a thin top-level router** — minimal disruption: wrap stdlib mux endpoints with chi, mount everything together.
2. **Implement stdlib-mux `Mount`** — add `MountServeMux(mux *http.ServeMux)` method on `Wiring` (or as a free function in authletas). Mount each AS endpoint with `mux.Handle("/oauth/authorize", ...)`, etc.

**Recommended:** Option 2 — fewer moving parts, preserves the existing stdlib-mux pattern. Go 1.22+ pattern matching (`{path...}`) makes this clean.

Wiring in `main.go`:

```go
authletWiring := authletas.TrySetup(ctx, db, store, cfg.OIDCClientID, cfg.OIDCClientSecret, slog.Default())

apiKeyMW := auth.APIKeyMiddleware(keyValidator)

var mcpHandler http.Handler = mcpServer.HTTPHandler()
if authletWiring != nil {
    mcpHandler = authletWiring.WWWAuth401()(authletWiring.DualAuth(apiKeyMW)(mcpHandler))
    mcpHandler = authletWiring.UserContextBridge()(mcpHandler)
} else {
    mcpHandler = apiKeyMW(mcpHandler)
}

mux.Handle("/mcp", mcpHandler)
mux.Handle("/mcp/", mcpHandler)
if authletWiring != nil {
    authletWiring.MountServeMux(mux)
    cleanupDone := authletWiring.RunCleanup(ctx)
    _ = cleanupDone
}
```

Verify the OAuth metadata endpoints work:
- `GET /.well-known/oauth-authorization-server`
- `GET /.well-known/oauth-protected-resource/mcp`
- `GET /jwks.json`
- `GET/POST /oauth/authorize`, `/oauth/token`, `/oauth/register`, `/oauth/idp/callback`, `/oauth/revoke`, `/oauth/userinfo`

**Branch:** `mount-authletas` (or combined with Phase C). **PR:** "feat: mount authletas on /mcp + /oauth".

## Phase E — Config + GCP Secret Manager keys

`internal/config/config.go` additions:

```go
AuthletMasterKey   string `env:"AUTHLET_MASTER_KEY"`              // 32-byte hex (64 chars)
GoogleClientID     string `env:"MEMORY_MCP_GOOGLE_CLIENT_ID"`
GoogleClientSecret string `env:"MEMORY_MCP_GOOGLE_CLIENT_SECRET"`
```

Issuer/Audience/redirect URI stay hardcoded constants in `internal/authletas/setup.go` — no `PUBLIC_BASE_URL` needed.

GCP Secret Manager provisioning in project `a11s-dev` (one-time):
- `memory-mcp-authlet-master-key` — `openssl rand -hex 32` (separate from hilo's master key — no shared key material)
- `memory-mcp-google-client-id` — from Google Console after Phase F
- `memory-mcp-google-client-secret` — from Google Console after Phase F

Memory-mcp deploys via k8s-forge on GKE (not ansible/vault like hilo), so secrets live in **GCP Secret Manager**, not HashiCorp Vault. Wire them into the `memory-mcp-secrets` `SecretConfig` in `a1/ops/k8s/forge/configs/factories/a11s/memory_mcp.py` via `gcp_secret(...)` refs.

## Phase F — Create memory-mcp Google OAuth client

Manual step in Google Console (mirrors hilo's setup, just a separate client):

1. Google Cloud Console → APIs & Services → Credentials → Create Credentials → OAuth client ID
2. Application type: Web application
3. Name: `memory-mcp.a11s.dev`
4. Authorized redirect URIs: `https://memory-mcp.a11s.dev/oauth/idp/callback`
5. Audience: same Workspace as hilo (`avantistudios.ai` Internal) — restricts who can sign in
6. Copy `client_id` and `client_secret` → GCP Secret Manager per Phase E

Note: in 2026-05-15 cutover the user opted to reuse Hilo's existing Google OAuth client and just added memory-mcp's redirect URI to its allow-list, rather than provisioning a separate client. Both approaches work; separate clients give better blast-radius isolation, shared client is one less rotation surface.

No Hilo interaction. No DB seed anywhere.

## Phase G — Deploy + verify

1. k8s-forge factory bump (MR on `a11s.ai/a1`) — add the three new env vars to the `memory-mcp-secrets` SecretConfig in `ops/k8s/forge/configs/factories/a11s/memory_mcp.py`.
2. Build + push image (Woodpecker on memory-system master after PRs merge).
3. Deploy via scheduled k8s-forge pipeline; manually `kubectl rollout restart deployment/memory-mcp -n a11s` if the running pod needs to pick up a fresh `:latest` (imagePullPolicy:Always pulls on container start, not while running).
4. Backfill `tenant_users`:
   ```sql
   INSERT INTO tenant_users (email, tenant_id, role) VALUES
   ('pe@avantistudios.ai', '8e1adf8b-3196-4b27-b6c3-6d62a14c4b1d', 'admin');
   ```
5. Smoke test PRM/AS metadata endpoints (curl from laptop).
6. MCP Inspector E2E:
   `npx @modelcontextprotocol/inspector@latest --url https://memory-mcp.a11s.dev/mcp`
   → DCR → /authorize → redirect to Google → return chain → /token → /mcp call works.
   (Topology change 2026-05-15: memory-mcp federates DIRECTLY to Google; no Hilo→Memory MCP coupling.)
7. Claude.ai Connector: add `https://memory-mcp.a11s.dev/mcp` as custom connector.

## Status (2026-05-15)

All phases complete. memory-mcp authlet is live in prod:
- AS metadata, PRM, JWKS, /oauth/*, and bearer-protected /mcp all serving.
- tenant_users backfilled (`pe@avantistudios.ai` → tenant `8e1adf8b-...`, role `admin`).
- CORS middleware on the OAuth/MCP surface (commit `f991b96`); without it MCP Inspector browser preflight failed on `/.well-known/oauth-authorization-server` and `/oauth/token`.
- MCP Inspector E2E succeeded (DCR → Google → JWT → tools list).
- Claude.ai Connector step remains user-driven (not yet attempted).

## Non-Goals (per design spec backlog)

- Consent screen
- Device-code grant (memory-agent CLI stays on API key)
- Per-scope MCP tool gating
- SCIM
- Multi-IdP fan-in

## Risks

| Risk | Mitigation |
|---|---|
| Stdlib mux can't host all AS endpoints | Implement `MountServeMux` in Phase D; fall back to chi if blocked |
| memory-agent breaks during DualAuth rollout | DualAuth routes API-key path unchanged; agent has no Authorization header → goes through legacy |
| Audience mismatch (Claude requests one URL, server checks another) | Resource Indicator `https://memory-mcp.a11s.dev/mcp` baked into PRM + RS audience check |
| Google rejects new client at Workspace level | Add memory-mcp's domain to the same Workspace allowlist hilo uses, or set External + Internal trusted |
