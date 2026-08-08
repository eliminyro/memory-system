# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Version sections below are generated automatically on each release from the
descriptions of the merged pull requests — see the pull request template for the
`## Release notes` convention that feeds them.

## [Unreleased]

<!-- INSERT NEW RELEASES BELOW -->

## [0.3.0] - 2026-08-08

### Features

- feat(release): PR-sourced changelog with Telegram-gated publish (#99) ([#99](https://github.com/eliminyro/memory-system/pull/99))
- feat(ui): amber-phosphor console redesign + create-doc & tenant-settings APIs (#75) ([#75](https://github.com/eliminyro/memory-system/pull/75))
- feat(authz): optional admin-only lock on tenant self-service (#73) ([#73](https://github.com/eliminyro/memory-system/pull/73))
- feat(authz): add personal-tenant owner relation (#72) ([#72](https://github.com/eliminyro/memory-system/pull/72))
- feat(ui): tab restructure, aggregated color-coded memories, per-tenant panel (#66) ([#66](https://github.com/eliminyro/memory-system/pull/66))
- feat: enforce tenant-type operation rules in the service (#65) ([#65](https://github.com/eliminyro/memory-system/pull/65))
- feat: MCP admin/ACL parity + cross-tenant get_related (#64) ([#64](https://github.com/eliminyro/memory-system/pull/64))
- feat: aggregate memory reads across the caller's readable tenants (#62) ([#62](https://github.com/eliminyro/memory-system/pull/62))
- feat: self-serve OAuth signup, tenant type, safe retention defaults (#61) ([#61](https://github.com/eliminyro/memory-system/pull/61))
- feat(ui): ACL management page + manager import/ACL affordances (#60) ([#60](https://github.com/eliminyro/memory-system/pull/60))
- feat: delegated ACL management backend (manager role, per-doc sharing) (#59) ([#59](https://github.com/eliminyro/memory-system/pull/59))
- feat(ui): dedicated import page with target-tenant picker (#58) ([#58](https://github.com/eliminyro/memory-system/pull/58))
- feat: bootstrap OAuth status + mode tabs; auto-register /ui OAuth client (#52) ([#52](https://github.com/eliminyro/memory-system/pull/52))

### Bug fixes

- fix(ci): green golangci-lint (drop dead funcs, pin config, node24 action) (#100) ([#100](https://github.com/eliminyro/memory-system/pull/100))
- fix: operational-resilience hardening (migrate lock, timeouts, panic recovery) (#97) ([#97](https://github.com/eliminyro/memory-system/pull/97))
- fix(security): stop telegram-token log leak, cap zip imports, bump vuln deps (#93) ([#93](https://github.com/eliminyro/memory-system/pull/93))
- fix: harden background workers + key rotation against multi-replica concurrency (#92) ([#92](https://github.com/eliminyro/memory-system/pull/92))
- fix(authz): manager-gate tenant toggles; enforce wildcard subject typing (#91) ([#91](https://github.com/eliminyro/memory-system/pull/91))
- fix: harden store concurrency + client lifecycle (signing keys, imports, grants) (#89) ([#89](https://github.com/eliminyro/memory-system/pull/89))
- fix: clamp lint scan bounds, gcp fail-fast, rate-limit proxy-depth warning (#88) ([#88](https://github.com/eliminyro/memory-system/pull/88))
- fix(ui): correct render-seq guard, add view error handling + dropzone drop (#87) ([#87](https://github.com/eliminyro/memory-system/pull/87))
- fix: stop MCP internal-error leak, fix update_section + search/apikey parity (#86) ([#86](https://github.com/eliminyro/memory-system/pull/86))
- fix: harden persistence integrity — partial path index, retention, import (#85) ([#85](https://github.com/eliminyro/memory-system/pull/85))
- fix(authz): atomic role changes, svc-admin reconcile, prune doc tuples on delete (#84) ([#84](https://github.com/eliminyro/memory-system/pull/84))
- fix(auth): physically purge expired refresh tokens (#83) ([#83](https://github.com/eliminyro/memory-system/pull/83))
- fix(ui): guard stale renders, stop poll leak, de-dup UI helpers (#82) ([#82](https://github.com/eliminyro/memory-system/pull/82))
- fix: spoof-safe rate-limit IP, reset ordering, http/service DRY (#81) ([#81](https://github.com/eliminyro/memory-system/pull/81))
- fix: unify MCP tool error mapping + de-dup error/tuple helpers (#80) ([#80](https://github.com/eliminyro/memory-system/pull/80))
- fix: hybrid-fusion boost + shared document path validation (#79) ([#79](https://github.com/eliminyro/memory-system/pull/79))
- fix: harden tenant-scoped data integrity (repository + service) (#78) ([#78](https://github.com/eliminyro/memory-system/pull/78))
- fix: correct OAuth per-user identity and by-id delete tenant scoping (#77) ([#77](https://github.com/eliminyro/memory-system/pull/77))
- fix(ui): wire standalone segmented rockers, not just descendants (#76) ([#76](https://github.com/eliminyro/memory-system/pull/76))
- fix(tenant): apply operator toggle defaults at tenant creation (#71) ([#71](https://github.com/eliminyro/memory-system/pull/71))
- fix(ui): active tab underline sits on the header's bottom line (#70) ([#70](https://github.com/eliminyro/memory-system/pull/70))
- fix(ui): collapse the "+ New tenant" form until toggled (#69) ([#69](https://github.com/eliminyro/memory-system/pull/69))
- fix: aggregate generate_index across the readable tenant set (#67) ([#67](https://github.com/eliminyro/memory-system/pull/67))
- fix: refuse self-serve auto-provision until the instance is bootstrapped (#63) ([#63](https://github.com/eliminyro/memory-system/pull/63))
- fix(ui): unify top-level nav on a single hash router (#57) ([#57](https://github.com/eliminyro/memory-system/pull/57))
- fix: clearer founding-admin-email help on the bootstrap page (#56) ([#56](https://github.com/eliminyro/memory-system/pull/56))
- fix: OAuth-first bootstrap success + break-glass key copy + MCP config snippets (#55) ([#55](https://github.com/eliminyro/memory-system/pull/55))
- fix: OAuth status as green/red badge; provider-neutral copy; no-store /ui assets (#54) ([#54](https://github.com/eliminyro/memory-system/pull/54))
- fix: no-store the bootstrap setup surface; style mode selectors as tabs (#53) ([#53](https://github.com/eliminyro/memory-system/pull/53))
- fix: bootstrap founding-admin as system admin; /ui empty-state + MCP connect (#51) ([#51](https://github.com/eliminyro/memory-system/pull/51))
- fix: ignore ErrRecordNotFound in gorm logger to stop import-worker poll spam (#50) ([#50](https://github.com/eliminyro/memory-system/pull/50))

### Other changes

- refactor: cut N+1s and duplication across authz, service, and HTTP surface (#90) ([#90](https://github.com/eliminyro/memory-system/pull/90))
- refactor(ui): drop the redundant Admin tab; Tenants tab owns tenant management (#68) ([#68](https://github.com/eliminyro/memory-system/pull/68))

---

Releases **v0.1.0** and **v0.2.0** predate this changelog; see the
[GitHub Releases](https://github.com/eliminyro/memory-system/releases) page for
their notes.
