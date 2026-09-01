## 1. Repository

- [ ] 1.1 Extend `DocumentRepository.List` (`internal/repository/document.go:133`) with slug-prefix, order-field, and direction parameters
- [ ] 1.2 Build the ordering clause from an allowlist mapping `slug` / `created_at` / `updated_at` / `title` to fixed column expressions, appending `id` as the final tiebreaker in every case
- [ ] 1.3 Apply the slug filter as `slug LIKE ?` with `%` and `_` escaped in the caller's value
- [ ] 1.4 Tests: prefix matches by month and year; a literal `%` in the prefix does not act as a wildcard; each order field sorts both directions; paging over a non-unique field (`updated_at` with duplicate timestamps) returns every row exactly once

## 2. Shared validation

- [ ] 2.1 One helper that validates `slug_prefix`, `order_by`, `order`, `limit`, `offset` and returns the repository arguments — called by both the MCP tool and the REST handler, so the escaping and allowlist cannot diverge
- [ ] 2.2 Reject an `order_by` outside the allowlist, an `order` other than `asc`/`desc`, a negative `limit` or `offset`, and `offset` supplied without `limit`
- [ ] 2.3 Tests: each rejection returns a validation error and runs no query

## 3. MCP surface

- [ ] 3.1 Add `slug_prefix`, `order_by`, `order`, `limit`, `offset` to `ListDocumentsInput` (`internal/mcp/tools.go:226`) with jsonschema descriptions
- [ ] 3.2 Pass them through `ListDocuments` (`tools.go:558`), keeping `limit=0` when the caller omits `limit` so the tool stays unbounded by default (design D2)
- [ ] 3.3 Tool description states that `slug_prefix` is the cheap path for date-identified documents and that paging is for large categories
- [ ] 3.4 Tests: omitting every new parameter returns today's result set in today's order; `category="journal"` + `order="desc"` + `limit=7` returns the seven most recent entries newest-first

## 4. REST surface

- [ ] 4.1 Accept and validate the same parameters on the browse endpoint in `internal/server/api.go`, through the shared helper
- [ ] 4.2 Tests: parity with the MCP tool for the same inputs and credentials; the same invalid `order_by` is rejected on both

## 5. Close-out

- [ ] 5.1 Document the parameters, the slug-prefix-versus-timestamp distinction, and the `id` tiebreaker in the docs routing section
- [ ] 5.2 `gofmt -w .` and `golangci-lint run` clean; full test suite green
