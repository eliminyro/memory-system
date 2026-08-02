package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/service"
)

// newImportUnitSvc builds a service with every DB-backed dependency nil: safe
// only for paths that never reach StoreDocument (unparseable-path skips and
// DocSource-level errors), which is exactly what these unit tests exercise.
// The parseable-doc-stored path needs a real database and is covered by the
// integration test.
func newImportUnitSvc() *service.MemoryService {
	return service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

// TestImportDocumentsSkipsUnparseablePath proves a path that does not parse
// to a category/slug is counted as Skipped, not Failed, and does not abort
// the import (spec scenario: *Skip an unparseable path*).
func TestImportDocumentsSkipsUnparseablePath(t *testing.T) {
	svc := newImportUnitSvc()
	src := func(emit func(path string, content []byte) error) error {
		return emit("", []byte("irrelevant"))
	}

	result, err := svc.ImportDocuments(context.Background(), uuid.New(), src)
	require.NoError(t, err)
	require.Equal(t, service.ImportResult{Skipped: 1}, result)
}

// TestImportDocumentsCountsMultipleSkips proves counts accumulate correctly
// across a DocSource that emits several unparseable paths in one pass.
func TestImportDocumentsCountsMultipleSkips(t *testing.T) {
	svc := newImportUnitSvc()
	paths := []string{"", "", ".md"}
	src := func(emit func(path string, content []byte) error) error {
		for _, p := range paths {
			if err := emit(p, nil); err != nil {
				return err
			}
		}
		return nil
	}

	result, err := svc.ImportDocuments(context.Background(), uuid.New(), src)
	require.NoError(t, err)
	require.Equal(t, service.ImportResult{Skipped: 3}, result)
}

// TestImportDocumentsSourceErrorPropagates proves a DocSource-level failure
// (e.g. the underlying walk itself erroring, distinct from a per-document
// problem) is returned to the caller as a fatal error rather than swallowed
// into the counts.
func TestImportDocumentsSourceErrorPropagates(t *testing.T) {
	svc := newImportUnitSvc()
	boom := errors.New("boom")
	src := func(emit func(path string, content []byte) error) error {
		return boom
	}

	result, err := svc.ImportDocuments(context.Background(), uuid.New(), src)
	require.ErrorIs(t, err, boom)
	require.Equal(t, service.ImportResult{}, result)
}
