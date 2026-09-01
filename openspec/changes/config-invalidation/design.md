## Context

Two cached surfaces, one existing and one arriving, share a refresh model that does not survive a
second replica.

- `internal/globalconfig/globalconfig.go:3` — "Load at boot; refresh after every write
  (write-through)." An atomic `Snapshot` is swapped on the writing replica. Nothing informs any other.
- `doc-type-policies` introduces a policy store with the same shape, where disagreement is sharper: one
  replica would reject a slug another accepts.
- `internal/service/import_worker.go:96` — "There is no notify channel, so a ticker is the trigger."
  The primitive has been wanted before.

No `LISTEN`, `NOTIFY`, or `pg_notify` appears anywhere in the codebase today. The database layer is
GORM over `postgres.Open` (`internal/database/database.go:23`), so a pgx connection is reachable
through the pooled `sql.DB`. Structural DDL that AutoMigrate cannot express already lives in
`database.go:208` onward as idempotent statements, which is where the trigger belongs.

Migrations already coordinate across replicas with an advisory lock (`database.go:73`), so multi-replica
operation is designed for, not hypothetical.

## Goals / Non-Goals

**Goals:**

- Replicas converge on configuration changes without polling and without a restart.
- One mechanism serving every cached surface, rather than one per surface.
- A replica serving stale configuration is visible.
- Single-replica behavior is unchanged.

**Non-Goals:**

- Converting `import_worker`'s ticker. Obvious third consumer, own recovery semantics, own change.
- Cache invalidation for anything document-shaped. This is configuration only — small, rarely written,
  fully reloadable.
- Removing write-through. See D3.

## Decisions

**D1 — Trigger-emitted notifications, not application-level `NOTIFY`.**

`pg_notify` from an `AFTER` trigger is delivered at commit. A listener reloading on receipt therefore
always reads a committed row.

An application-level `NOTIFY` issued outside the write transaction races: the notification can arrive
and the reload can run before the write is visible, leaving every other replica caching the *old* value
with no further notification coming. That failure is silent and permanent, which is the worst
combination.

A trigger also covers writes that bypass the application. `doc-type-policies` declares raw SQL editing
unsupported, but a trigger costs nothing and removes the question.

**D2 — The notification is a signal; the reload reads the database.**

No state travels in the payload. Reload functions re-read their table, so a notification cannot deliver
a partial or stale view, and the 8000-byte payload limit never matters. The tables involved hold one
row and eight rows.

**D3 — Write-through is kept, not replaced.**

The listener could be the only path, giving one mechanism instead of two. Rejected: a replica's own
writes would then depend on its listener being healthy, so a listener failure would break the replica
that is actively being administered. Keeping write-through means a listener failure degrades other
replicas' freshness but never the writer's correctness.

Reloads are idempotent, so the writer reloading twice — once write-through, once on its own
notification — is a wasted query and nothing more.

**D4 — `LISTEN` before the initial load.**

Load-then-listen leaves a gap: a change committed inside it notifies nobody, and the stale value
survives until restart. Listen-then-load can only over-reload, which is harmless.

**D5 — Unconditional reload on reconnect.**

PostgreSQL does not queue notifications for a disconnected client. A reconnecting listener cannot know
what it missed, so tracking becomes guesswork. Reloading everything on reconnect is a handful of
queries against tiny tables and converges by construction.

*Alternative considered:* a monotonic version column per config table, compared on reconnect to decide
whether to reload. Strictly more precise, and pointless when the reload it avoids costs one query
against a nine-row table.

**D6 — Registration, so the listener stays generic.**

Each cached surface registers `(channel, reloadFunc)`. The listener owns the pinned connection, the
dispatch loop, the reconnect logic, and health. It knows nothing about configuration semantics.

This is what makes it worth building once rather than per-surface: `doc-type-policies` registers its
store, `globalconfig` registers its loader, and the import worker can register later without touching
the listener.

**D7 — Failure is surfaced on `/~/ready`, not swallowed.**

A listener that cannot reconnect leaves that replica serving stale configuration forever. Reporting it
on the existing readiness endpoint (`internal/server/handler.go:116`) lets an orchestrator route around
it, which is the correct response — the replica is functional but no longer trustworthy for
configuration-dependent behavior.

*Alternative considered:* a slow poll as a backstop when the listener is down. That reintroduces the
timer this design exists to remove, and hides the failure rather than reporting it.

**D8 — Whether a dead listener fails readiness is a config flag, defaulting to off.**

`require_config_listener` on `instance_config`, seeded from an env var like the other runtime globals and
editable through `/admin/config` and the web UI.

Off by default because the failure is only meaningful with peers. A single replica gets every change
write-through, so it has nothing to miss; failing readiness there removes the only pod from its Service
over a fault that changed no behavior. With several replicas a stale one enforces different rules from
its peers, so it should be routed around. The operator knows the replica count; the server does not need
to infer it.

`/~/health` is deliberately untouched — it is liveness, and failing it would restart a process that is
working fine. A restart would happen to fix a dead listener, which makes it a tempting mechanism and a
bad one.

*Known asymmetry, accepted:* a replica whose listener is already dead will not receive the notification
for a change to this very flag, so enabling it does not reach an already-failed replica until restart.
The flag only governs readiness reporting, never the listener itself, so there is no circularity beyond
that.

## Risks / Trade-offs

- **A pinned connection is a connection the pool cannot use.** → One, for the process lifetime.
  Negligible against any realistic pool size, and it is the standard cost of `LISTEN`.
- **A silently dead listener is the whole failure mode.** → D7 puts it on `/~/ready` and logs
  reconnection failures. This is the risk the design is most exposed to, so it gets the most explicit
  treatment.
- **Triggers are invisible in Go code.** Someone reading the write path will not see the notification
  happen. → The DDL sits with the other structural statements in `database.go`, and each registering
  cache documents the channel it depends on.
- **Reload storms if a config table were written frequently.** → These tables hold one and eight rows
  and are written by admins, not by traffic. If that ever changes, debouncing is the fix.
- **Testing needs a real PostgreSQL**, since notifications cannot be faked at the `database/sql` layer.
  → The repo already runs integration tests against a real database.

## Migration Plan

1. Trigger function and the `instance_config` trigger, added as idempotent DDL alongside the existing
   structural statements.
2. Listener package; start it in `cmd/server/main.go` before the initial config load, stop it on
   shutdown.
3. `globalconfig` registers its loader. Write-through unchanged.
4. `/~/ready` reports listener state.
5. `doc-type-policies` registers its store and creates its own trigger with its table.

All four changes ship inside the re-cut v1.0.0, so there is no released intermediate state and no
release ordering to observe — only implementation ordering.

Rollback: drop the trigger and stop starting the listener. Write-through remains, so behavior returns
exactly to today's.

## Open Questions

- Does the import worker's ticker become a listener registration, and does that change its recovery
  semantics? Out of scope here, worth answering before a third consumer appears.
