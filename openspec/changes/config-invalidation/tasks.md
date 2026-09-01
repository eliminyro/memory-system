## 1. Trigger

- [ ] 1.1 `pg_notify` trigger function, parameterised by channel name via `TG_ARGV`, so one function serves every config table
- [ ] 1.2 `AFTER INSERT OR UPDATE OR DELETE` trigger on `instance_config`, added as idempotent DDL alongside the existing structural statements in `internal/database/database.go:208`
- [ ] 1.3 Test against a real database: a committed write emits a notification; a rolled-back write emits none; the notification is not delivered before commit

## 2. Listener

- [ ] 2.1 New package holding one `sql.Conn` pinned from the pool for the process lifetime, unwrapped via `Raw()` to the pgx connection to wait for notifications
- [ ] 2.2 Registration API — `(channel, reloadFunc)` — with the listener owning the connection, dispatch, reconnect, and health, and knowing nothing about configuration semantics
- [ ] 2.3 Dispatch loop: on notification, run the registered reload; ignore unregistered channels without error
- [ ] 2.4 Reconnect with backoff; on every successful reconnect re-establish all `LISTEN` registrations and reload **every** registered cache unconditionally
- [ ] 2.5 Clean shutdown releasing the pinned connection
- [ ] 2.6 Tests: registered channel reloads; unregistered channel ignored; one connection serves multiple channels; a dropped connection reconnects and reloads everything

## 3. Startup ordering

- [ ] 3.1 Start the listener and establish `LISTEN` in `cmd/server/main.go` **before** any cache performs its initial load
- [ ] 3.2 Test: a row committed between listener start and initial load is not lost — the cache ends up with the new value

## 4. globalconfig registration

- [ ] 4.1 `internal/globalconfig` registers its loader against the `instance_config` channel
- [ ] 4.2 Leave the write-through refresh in place; confirm the reload is idempotent so a double reload changes nothing
- [ ] 4.3 Tests: a write on one connection is observed by a second listener; the writing path still refreshes without waiting for the notification

## 5. Health reporting and the readiness toggle

- [ ] 5.1 `require_config_listener bool` on `models.InstanceConfig` (default false) with its JSON tag, plus the `InstanceConfigPatch` field
- [ ] 5.2 Seed it from an env var in the one-time globals seed (`internal/database/database.go:259-283`), so it joins the existing `globals_seeded` path and an admin edit survives restarts
- [ ] 5.3 Expose it on `globalconfig.Accessor`, and in `GET`/`PATCH /admin/config` so the web UI config surface picks it up like the other toggles
- [ ] 5.4 `/~/ready` (`internal/server/handler.go:116`) reports not-ready on a dead listener only when the flag is true; `/~/health` is unaffected in either case
- [ ] 5.5 Log reconnection failures at a level that surfaces them regardless of the flag
- [ ] 5.6 Tests: default false keeps ready green with a dead listener and logs; true reports not-ready; health stays green either way; ready recovers on reconnect; env seeds once and an admin patch survives a restart

## 6. Close-out

- [ ] 6.1 Document the channel names, the registration API, and the ordering and reconnect guarantees — specifically that `LISTEN` precedes the initial load and that reconnect reloads unconditionally
- [ ] 6.2 Note in `doc-type-policies` that its store registers here and its trigger ships with its migration
- [ ] 6.3 `gofmt -w .` and `golangci-lint run` clean; full test suite green
