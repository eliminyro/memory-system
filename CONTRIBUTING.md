# Contributing

Thanks for your interest in memory-system! This guide covers the dev loop and the
conventions the project (and its release automation) relies on.

## Development

You need Go (the version in [`go.mod`](go.mod)) and a PostgreSQL with the
[`pgvector`](https://github.com/pgvector/pgvector) extension. The bundled
[`docker-compose.yml`](docker-compose.yml) brings up a full stack for manual testing.

```bash
# format + lint (CI enforces both)
gofmt -w .
golangci-lint run

# unit tests
go test ./...

# integration tests need a pgvector database; -p 1 serializes packages so
# parallel AutoMigrate runs don't race on the shared database.
export TEST_DATABASE_URL='postgres://memory:memory@localhost:5432/memory?sslmode=disable'
go test -tags=integration -p 1 ./...
```

## Pull requests

- Branch from `master`; keep one topic per PR.
- CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) must be green: gofmt,
  golangci-lint, unit + integration tests, `go build`, and a boot smoke test.
- PRs are **squash-merged**, so the PR title becomes the commit message.

## Commit messages — Conventional Commits

The project uses [Conventional Commits](https://www.conventionalcommits.org/), and
the release automation derives the next version from them:

| Prefix | Bump |
|--------|------|
| `fix:` | patch |
| `feat:` | minor |
| `feat!:` / a `BREAKING CHANGE:` footer | major |
| `chore:` / `docs:` / `refactor:` / `test:` … | none (no release) |

So a PR titled `feat: add X` cuts a minor release when it merges; `chore:`/`docs:`
changes ship without a version bump. Write the description in the imperative
("add", not "added").

## Bugs and security

Open a GitHub issue for bugs. For **security vulnerabilities**, do not open a public
issue — follow [SECURITY.md](SECURITY.md) to report privately.
