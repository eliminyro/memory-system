## ADDED Requirements

### Requirement: Shared invalidation listener

The system SHALL run one listener holding a dedicated PostgreSQL connection, `LISTEN`ing on
configuration-change channels and invoking the reload function registered for each.

Cached surfaces SHALL register a channel name and a reload function. The listener owns the connection
and the dispatch; it SHALL NOT know what any particular cache contains.

The connection SHALL be pinned out of the pool for the listener's lifetime and SHALL NOT be returned
between notifications, because `LISTEN` registrations are per-connection.

#### Scenario: A registered cache reloads on notification

- **WHEN** a notification arrives on a registered channel
- **THEN** that cache's reload function runs and the new values are served

#### Scenario: Unregistered channels are ignored

- **WHEN** a notification arrives on a channel with no registration
- **THEN** it is discarded without error

#### Scenario: One connection serves every channel

- **WHEN** several caches are registered
- **THEN** the listener holds a single connection listening on all of their channels

### Requirement: Triggers emit the notifications

Configuration tables SHALL carry an `AFTER INSERT OR UPDATE OR DELETE` trigger calling `pg_notify` with
that table's channel. `instance_config` gets one in this change; `doc_type_policies` gets one with its
own migration.

A trigger is used rather than an application-level `NOTIFY` call because notifications from a trigger
are delivered when the transaction commits. A listener therefore always observes the committed row. An
application-level notify sent outside the transaction could be received before the write is visible.

The notification is a signal. Its payload SHALL NOT be trusted as data, and the reload SHALL read the
current state from the database.

#### Scenario: Commit ordering

- **WHEN** a configuration row is updated inside a transaction
- **THEN** no notification is delivered until that transaction commits, and a listener reloading on receipt observes the committed value

#### Scenario: Rollback emits nothing

- **WHEN** a configuration write is rolled back
- **THEN** no notification is delivered

#### Scenario: Payload is not data

- **WHEN** a notification carries a payload
- **THEN** the reload ignores it and reads current state from the database

### Requirement: Listen before the initial load

The listener SHALL establish its `LISTEN` registrations **before** any cache performs its initial load.

Loading first would leave a window in which a change committed between the load and the `LISTEN` is
never observed — the notification would be sent to nobody and the stale value would persist until
restart.

#### Scenario: Change during startup is not lost

- **WHEN** a configuration row is committed after the listener starts but before a cache finishes its initial load
- **THEN** the resulting notification is processed and the cache reloads

### Requirement: Reconnection reloads unconditionally

If the listener's connection drops, the system SHALL reconnect, re-establish every `LISTEN`, and then
reload **every registered cache**, whether or not any notification is known to have been missed.

Notifications delivered while disconnected are not queued for a reconnecting client. Reloading
unconditionally is the only way to converge without tracking what was missed.

#### Scenario: Missed notification is recovered

- **WHEN** the connection drops, a configuration row changes, and the listener then reconnects
- **THEN** every registered cache reloads and serves the new value

#### Scenario: Reconnection is retried

- **WHEN** reconnection fails
- **THEN** the listener retries with backoff rather than exiting

### Requirement: Write-through is retained

The write path SHALL continue to refresh the writing replica's cache in process, as it does today. The
listener supplements it and does not replace it.

Reloads SHALL be idempotent, so the writing replica receiving its own notification and reloading a
second time changes nothing.

#### Scenario: Writer does not wait for its own notification

- **WHEN** an admin writes a configuration change
- **THEN** the writing replica serves the new value immediately, without depending on the listener

#### Scenario: Other replicas converge

- **WHEN** the same write commits
- **THEN** every other replica reloads on the notification and serves the new value

#### Scenario: Double reload is harmless

- **WHEN** the writing replica reloads write-through and then again on its own notification
- **THEN** the served values are unchanged

### Requirement: A failed listener is visible, and whether it fails readiness is configurable

A listener that cannot maintain its connection means that replica serves stale configuration
indefinitely. The system SHALL log a reconnection failure at a level that surfaces it, and SHALL report
listener state on `/~/ready`.

Whether a dead listener makes `/~/ready` fail SHALL be controlled by
`require_config_listener` on `instance_config` — seeded from an env var like the other runtime globals,
editable through `/admin/config` and the web UI, **defaulting to false**.

The default is off because a single replica receives every configuration change write-through: it has no
peers whose edits it could be missing, so a dead listener is harmless there and failing readiness would
take the only pod out of service over a fault that changed nothing. A multi-replica deployment enables
it, because there a stale replica enforces different behavior from its peers.

`/~/health` SHALL NOT be affected. That endpoint is liveness, and failing it would make an orchestrator
restart a process that is otherwise entirely healthy.

Note the one asymmetry, which is acceptable: a replica whose listener is already dead will not receive
the notification for a change to this flag. Enabling it does not reach an already-failed replica until
that replica restarts.

#### Scenario: Default does not fail readiness

- **WHEN** the listener has failed, cannot reconnect, and `require_config_listener` is false
- **THEN** `/~/ready` reports ready, and the failure is logged

#### Scenario: Enabled, readiness fails

- **WHEN** the listener has failed, cannot reconnect, and `require_config_listener` is true
- **THEN** `/~/ready` reports the replica as not ready

#### Scenario: Health is never affected

- **WHEN** the listener has failed under either setting
- **THEN** `/~/health` reports healthy

#### Scenario: Recovery clears it

- **WHEN** a failed listener reconnects
- **THEN** `/~/ready` reports ready again, and every registered cache has been reloaded

#### Scenario: Flag is env-seeded and admin-editable

- **WHEN** the instance starts fresh with the env var set, and an admin later changes the value through `/admin/config`
- **THEN** the env value seeds the row once and the admin's change survives subsequent restarts

#### Scenario: Single replica is unaffected

- **WHEN** the server runs as a single replica and the listener is healthy
- **THEN** behavior is identical to today: write-through serves every change, and notifications cause redundant, harmless reloads

