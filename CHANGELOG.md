# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Version sections below are generated automatically on each release from the
descriptions of the merged pull requests — see the pull request template for the
`## Release notes` convention that feeds them.

## [Unreleased]

## [1.1.0] - 2026-09-05

### Added

- Opt-in expired-document retention sweep: hard-deletes documents past their `doc_type`'s `expiration_age_days` plus a grace window that are also access-cold, unpinned, and prunable — with a `deletion_events` audit trail and a `lint_memory` dry-run to preview candidates. Off by default (`retention_sweep_enabled`).
- Per-tenant opt-in usage metrics: an append-only access/verify/cleanup event log with configurable retention, live stale/expired gauges, an admin dashboard, and a Prometheus-shaped aggregation backend.

## [1.0.0] - 2026-08-30

Initial public release.

### Added

- MCP server (Streamable-HTTP) giving AI agents persistent, semantic long-term memory.
- Hybrid retrieval: pgvector HNSW cosine search fused with GIN-indexed full-text search, RRF fusion plus a tunable MMR diversity re-rank.
- Pluggable embedding providers: Ollama, GCP Vertex AI, OpenAI-compatible, AWS Bedrock, and a deterministic fake provider for tests.
- Dual authentication — long-lived API keys and OAuth 2.1 / OIDC — resolving to a unified subject checked by a Zanzibar-style, relationship-based authorization engine.
- Multi-tenant isolation with a shared common pool; reads aggregate across a caller's tenants, writes stay single-tenant.
- Per-`doc_type` staleness signal (off / advisory / hard) and an optional near-duplicate write guard; agent-driven retirement (supersede purges content into a lineage tombstone, update, delete) with no timed sweep.
- Web console (`/ui`) for browsing/editing memories and managing tenants, members, API keys, and global config; token-gated first-run bootstrap over HTTP; archive import via the admin UI or `POST /import`.
- Hardened HTTP edge: request-size limits, token-bucket rate limiting, strict CSP, unauthenticated health/readiness endpoints.
