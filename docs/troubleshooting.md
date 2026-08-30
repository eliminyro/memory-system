# Troubleshooting

Common startup and connection errors, keyed to the message you see. Each entry points at
the doc that covers the fix in full.

## Bootstrap token missing from logs, or `already bootstrapped`

The one-time bootstrap token is logged **once at WARN** under the field `bootstrap_token`
when an un-bootstrapped instance starts. It is regenerated every boot and never persisted,
so a token you missed is gone — restart the server to log a fresh one, or raise
`LOG_LEVEL` if WARN is being filtered. A `POST /bootstrap` after an admin already exists
returns `409 already bootstrapped`, and the `/bootstrap` surface then 404s. To provision a
new first admin, re-arm bootstrap with `MEMORY_RESET=1` (clears the admin set and re-arms
the token on the next boot). See [administering.md](administering.md#first-run-bootstrap-http)
and [administering.md](administering.md#break-glass-reset), plus the env table in the
[README](../README.md).

## `embedding dimension mismatch` or provider/model-change boot refusal

The server freezes the embedding identity (provider + model + dimension) and refuses to
start if it changes under existing vectors, because similarity, dedup, and retention would
silently corrupt. Restore the previous `EMBEDDING_PROVIDER` / model / `EMBEDDING_DIMENSIONS`,
or re-embed the whole corpus. See
[embedding-providers.md](embedding-providers.md#changing-your-embedding-model).

## OAuth credentials one-sided, or missing/relative `PUBLIC_BASE_URL`

The OAuth path is opt-in and all-or-nothing: setting only one of
`MEMORY_MCP_GOOGLE_CLIENT_ID` / `MEMORY_MCP_GOOGLE_CLIENT_SECRET` is a fatal config error.
When both are set the OAuth path turns on and `PUBLIC_BASE_URL` must be an absolute
`http(s)` origin with a host and no path — a missing or relative value fails config load.
See the OAuth section of the [README](../README.md) and
[architecture.md](architecture.md#configuration).

## Ollama `model not found` on the first `store_memory`

The embedding model has not been pulled into Ollama yet. Pull it:

```sh
docker compose exec ollama ollama pull nomic-embed-text
```

The bundled `docker-compose.yml` also runs an `ollama-init` one-shot that pulls
`nomic-embed-text` before `memory-mcp` starts, so a clean `docker compose up` should not
hit this. Pull manually only if you run Ollama outside that compose file.

## `401` on `/mcp`

The request is missing or carrying a bad bearer token. `/mcp` requires
`Authorization: Bearer mmcp_...` (an API key) or a valid OAuth JWT. Check the token is
current and copied whole. See [connecting-clients.md](connecting-clients.md).

## `/ui` or other endpoints returning `404` before bootstrap

Expected. Before the first admin is provisioned, `/ui` 404s and only `/bootstrap` and the
unauthenticated health endpoints (`/~/health`, `/~/ready`, `/~/version`) are served.
Complete the bootstrap and the console comes up. See
[administering.md](administering.md#first-run-bootstrap-http).
