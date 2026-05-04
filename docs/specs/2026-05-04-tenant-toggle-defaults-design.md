# Operator-Configurable Tenant-Toggle Defaults — Design Spec

**Date:** 2026-05-04
**Repo:** `github.com/eliminyro/memory-system`
**Scope:** Make the three per-tenant feature toggles (`staleness_mode`, `duplicate_guard`, `cleanup_scan_enabled`) operator-configurable at server startup. Flip the safe baseline so a fresh deploy is invisible to existing users.

## Motivation

Master is ready to deploy to the Avanti dev environment, but the current `staleness_mode` default is `'advisory'`. Advisory mode mutates response shapes (warning fields on stale-and-code-path sections), which can surprise older clients that don't parse those fields.

The other two toggles (`duplicate_guard`, `cleanup_scan_enabled`) already default to `false`, but they live in the same comment block ("All default to the safest, least-surprising behavior") and benefit from the same operator control surface.

Goal: deploy master without behavior change for existing tenants, and let the operator pick the deploy-time defaults explicitly.

## Surface

### CLI flag

```
memory-server --opts staleness=off,duplicate_guard=false,cleanup_scan_enabled=false
```

### Env var fallback

```
MEMORY_DEFAULT_OPTS="staleness=off,duplicate_guard=false,cleanup_scan_enabled=false"
```

CLI flag takes precedence over env var when both are set. Both omitted → safe baseline (`staleness=off`, `duplicate_guard=false`, `cleanup_scan_enabled=false`).

### Format

- Comma-separated `key=value` pairs.
- Whitespace around tokens is tolerated.
- Keys (3 valid): `staleness`, `duplicate_guard`, `cleanup_scan_enabled`.
- Values:
  - `staleness ∈ {off, advisory, hard}` — case-insensitive.
  - Booleans accept `true|false|1|0` — case-insensitive.
- Unknown key or invalid value → server fails to start with a clear error message naming the offending token.
- Partial specs allowed: omitted keys use safe baseline.

## Where the values land

The operator-configured defaults are applied at three layers, top to bottom of the write path:

1. **GORM struct tags** — `internal/models/tenant.go`. Baseline defaults flipped to the safest values:
   ```go
   StalenessMode      string `gorm:"size:16;not null;default:'off'"`
   DuplicateGuard     bool   `gorm:"not null;default:false"`
   CleanupScanEnabled bool   `gorm:"not null;default:false"`
   ```
   This is the compile-time fallback and what `AutoMigrate` writes as the column default.

2. **Postgres column DEFAULT** — `internal/database/database.go`. After `AutoMigrate`, run an `ALTER TABLE tenants ALTER COLUMN <col> SET DEFAULT <operator-value>` for each toggle, using the operator's chosen values from config. So a raw `INSERT INTO tenants (name) VALUES (...)` (admin path, future migrations, manual SQL) gets the operator's deploy-time default at the DB level.

3. **In-code tenant creation** — `internal/service/memory.go` (and any other path that constructs `Tenant{}`). When a `Tenant` struct is built with zero-values for these fields, the create path fills them from the operator config before insert. Belt-and-suspenders so we never rely on the DB default catching us.

## Consistency fix

`internal/service/memory.go:132` currently falls back to `StalenessModeAdvisory` when a tenant row contains an invalid `staleness_mode` value. The two adjacent fail-safe branches (lines 122 and 128) already fall back to `StalenessModeOff` per the comment "Fail safe: ... so we never accidentally refuse content due to an infra glitch." Flip the line-132 fallback to `StalenessModeOff` for consistency.

## File touch list

| File | Change |
|---|---|
| `internal/config/config.go` | Add `TenantDefaults` struct fields + parser; read `MEMORY_DEFAULT_OPTS`. |
| `internal/config/config_test.go` (new) | Parser table-test: valid combos, partial specs, unknown keys, invalid values, CLI-vs-env precedence. |
| `cmd/server/main.go` | Add `--opts` flag; parse; merge into config (CLI wins). Fail-fast on parse error. |
| `internal/models/tenant.go` | Flip baseline GORM defaults: `'off'`, `false`, `false`. |
| `internal/database/database.go` | After `AutoMigrate`, run `ALTER COLUMN ... SET DEFAULT` for the three toggles using config values. |
| `internal/service/memory.go` | Line 132: invalid-value fallback → `StalenessModeOff`. Tenant-create path: fill zero-values from config. |

## Migration impact

- **Prod (Avanti dev infra) — not yet deployed.** First migration will `ADD COLUMN ... NOT NULL DEFAULT 'off'`, so all existing tenant rows get `'off'`. No behavior change for any caller.
- **Existing dev/staging DBs that already migrated with `'advisory'`.** Rows keep their current values. No automatic rewrite of `'advisory'` → `'off'` because that would clobber any tenant that explicitly opted into advisory. Operator flips per-tenant via `update_my_tenant_settings` if desired.
- **Postgres `ALTER COLUMN ... SET DEFAULT`** is metadata-only on Postgres — no row rewrite, no lock escalation past `AccessExclusive` for a brief instant.

## Backwards compatibility

- Older clients (no SDK, no agents): unaffected. Toggles default to `off`/`false` so response shapes stay flat.
- Existing tenants on any DB: unaffected — only the column default for new rows changes, plus the operator-configured default applies forward only.
- New tenants created post-deploy: get whatever the operator configured at startup.

## Testing

- **Parser unit tests** (table-driven) in `internal/config/config_test.go`:
  - All valid combinations and case-insensitive values
  - Partial specs (one key, two keys, all three)
  - Unknown key fails with key name in error
  - Invalid `staleness` value fails with value in error
  - Invalid bool value fails with value in error
  - Whitespace tolerance
- **CLI-vs-env precedence test**: env set + flag set → flag wins.
- **No DB integration test** for `ALTER COLUMN SET DEFAULT` — covered by manual deploy verification.

## Branch + commit + workflow

- Branch from `master`: `chore/configurable-tenant-toggle-defaults`.
- One commit, message: `chore: configurable defaults for tenant toggles`.
- Format: `gofmt -w .`. Lint: `golangci-lint run`.
- Push → open PR. Pipeline merges.

## Out of scope

- Per-tenant override semantics (already exist via `update_my_tenant_settings`).
- Migrating existing `'advisory'` rows to `'off'` (clobbers explicit opt-ins).
- `staleness_thresholds` per-doc-type tuning (separate concern).
- Adding `--opts` to the `cmd/import` and `cmd/agent` binaries (server-only concern).
