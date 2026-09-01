## Why

Cached configuration is refreshed write-through: the replica that performs a write updates its own
in-memory copy and no other replica learns anything. `internal/globalconfig/globalconfig.go:3` states
the pattern outright — "Load at boot; refresh after every write (write-through)."

On a single replica that is correct and cheap. On more than one it means **replicas silently disagree**.
An admin changes `instance_config` on replica A; replica B keeps serving the old snapshot until it
restarts. This is not a lag — nothing converges on its own.

That hole exists today for `instance_config`. `doc-type-policies` is about to add a second cached
surface with the same shape, where disagreement is sharper still: one replica would reject a slug
another accepts, or run a duplicate guard another skips.

There is no notification primitive to build on. `internal/service/import_worker.go:96` says so
explicitly — "There is no notify channel, so a ticker is the trigger." Three consumers want the same
missing piece.

## What Changes

- A shared invalidation listener holding one dedicated PostgreSQL connection, `LISTEN`ing for
  configuration-change notifications and reloading the affected cache.
- `AFTER INSERT OR UPDATE OR DELETE` triggers on `instance_config` and (once it exists)
  `doc_type_policies`, calling `pg_notify` with a channel-specific signal.
- Registration so each cached surface supplies its own reload function; the listener owns the
  connection and the dispatch, not the knowledge of what to reload.
- Write-through refresh is **kept**. The writing replica still updates in process immediately, so a
  dead listener never delays the replica that made the change. Reloads are idempotent, so receiving
  your own notification is harmless.
- Listener health is surfaced on `/~/ready`. A listener that cannot reconnect means that replica is
  serving stale configuration, and that must be visible rather than silent.

Ordering and reconnection are specified, because both are easy to get subtly wrong:

- `LISTEN` is established **before** the initial load, so a change landing during startup is not missed.
- On reconnect the listener reloads **unconditionally**, because notifications sent while disconnected
  are gone.
- Notifications are signals, not payloads. The listener reloads from the database rather than trusting
  anything carried in the message.

Out of scope: converting `import_worker`'s ticker to the new channel. It is the obvious third consumer,
but it has its own recovery semantics and deserves its own change.

## Capabilities

### New Capabilities

- `config-invalidation`: the listener, the triggers, registration of reloadable caches, ordering and
  reconnection guarantees, and health reporting.

### Modified Capabilities

None. `openspec/specs/` holds no spec files, so the current write-through behavior has nothing to delta
against; it is captured in the spec as the behavior that is retained alongside the listener.

## Impact

- New package for the listener — one pinned `sql.Conn` obtained from the pool, unwrapped to the pgx connection to wait for notifications.
- `internal/database/database.go` — trigger and function DDL for `instance_config`, idempotent like the other structural DDL there.
- `internal/globalconfig/globalconfig.go` — registers its loader with the listener; write-through path unchanged.
- `cmd/server/main.go` — starts the listener before the initial config load and stops it on shutdown.
- `internal/server/handler.go:116` — `/~/ready` reports listener state.
- `doc-type-policies` — registers its policy store the same way, and its trigger ships with its migration.

Data: two triggers and a notify function. No table or column changes.

Callers: none. Single-replica behavior is unchanged; multi-replica gains convergence.
