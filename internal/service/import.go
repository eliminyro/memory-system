package service

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/models"
)

// DocSource is a push-style iterator over documents to ingest: it walks its
// own source — a filesystem directory, an in-memory unzipped archive, ... —
// and calls emit once per document found, as (relative path, raw content).
// A non-nil error returned by the DocSource itself aborts the whole import
// (e.g. the root path does not exist); per-document problems are handled
// inside the emit callback ImportDocuments passes in and never abort the
// walk. The CLI (Task 7.5) wraps filepath.Walk and the HTTP worker (Task 7.4)
// wraps an unzipped-archive walk; both drive the same ImportDocuments core
// through this one shape.
type DocSource func(emit func(path string, content []byte) error) error

// ImportResult tallies a bulk import: Imported counts documents successfully
// parsed and stored, Skipped counts items whose path did not parse into a
// category/slug (not fatal), and Failed counts parseable items whose
// StoreDocument call errored (also not fatal — one bad file must not abort
// the batch, matching cmd/import's current behavior).
type ImportResult struct {
	Imported int
	Skipped  int
	Failed   int
}

// ImportDocuments is the shared ingest core (design D8; spec: *Shared ingest
// core*). It establishes tenantID on the context, then drains src and for
// each (path, content) pair parses category/subcategory/slug and stores the
// document with the duplicate guard bypassed (force=true) — StoreDocument
// itself seeds the document's document#tenant authz tuple (lifecycle
// seeding), so no separate seed step is needed here. This is exactly the loop
// cmd/import/main.go currently inlines, lifted so both the CLI and the HTTP
// import worker can drive it via different DocSource implementations.
func (s *MemoryService) ImportDocuments(ctx context.Context, tenantID uuid.UUID, src DocSource) (ImportResult, error) {
	var result ImportResult
	ctx = auth.WithTenantID(ctx, tenantID)

	err := src(func(path string, content []byte) error {
		category, subcategory, slug := parseImportPath(path)
		if category == "" || slug == "" {
			slog.Default().Warn("skipping unparseable path", "path", path)
			result.Skipped++
			return nil
		}

		// Bulk import bypasses the duplicate guard — the operator's intent is to
		// ingest files as-authored, not to prompt on every near-duplicate.
		if _, err := s.StoreDocument(ctx, category, subcategory, slug, string(content), true, "bulk import", nil); err != nil {
			slog.Default().Warn("failed to import document", "path", path, "error", err)
			result.Failed++
			return nil
		}

		result.Imported++
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("import: %w", err)
	}
	return result, nil
}

// parseImportPath converts a relative document path (e.g.
// "learnings/go/gorm.md", possibly OS-separated) into category/subcategory/
// slug: it trims a trailing ".md" and normalizes separators before deferring
// to models.ParsePath — the same preprocessing cmd/import/main.go's local
// parsePath helper performed.
func parseImportPath(path string) (category string, subcategory *string, slug string) {
	path = strings.TrimSuffix(path, ".md")
	path = filepath.ToSlash(path)
	return models.ParsePath(path)
}
