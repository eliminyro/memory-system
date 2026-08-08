//go:build integration

package service_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestListDocuments_Pagination exercises the limit/offset windows, the
// limit<=0 unbounded path, and stable ordering across pages (designs D2/D6):
// a paged walk must cover exactly the unbounded set with no duplicated or
// skipped row.
func TestListDocuments_Pagination(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	category := "pg-" + uuid.NewString()[:8] // unique category => deterministic count

	const total = 7
	for i := 0; i < total; i++ {
		slug := fmt.Sprintf("p%02d-%s", i, uuid.NewString()[:8])
		_, err := f.svc.StoreDocument(ctx, category, nil, slug, "# T\n\n## H\nbody", true, "seed", nil)
		require.NoError(t, err)
	}

	// limit<=0 returns the whole category (today's behavior).
	all, err := f.svc.ListDocuments(ctx, &category, nil, nil, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, total, "limit<=0 must return every document")

	allNeg, err := f.svc.ListDocuments(ctx, &category, nil, nil, -1, 0)
	require.NoError(t, err)
	require.Len(t, allNeg, total, "a negative limit is also unbounded")

	// A limit smaller than the set returns exactly one page.
	page0, err := f.svc.ListDocuments(ctx, &category, nil, nil, 3, 0)
	require.NoError(t, err)
	require.Len(t, page0, 3, "limit=3 must return one page of 3")

	// Offset windows the result.
	tail, err := f.svc.ListDocuments(ctx, &category, nil, nil, 3, 6)
	require.NoError(t, err)
	require.Len(t, tail, 1, "offset=6 leaves a final short page of 1")

	beyond, err := f.svc.ListDocuments(ctx, &category, nil, nil, 3, total)
	require.NoError(t, err)
	require.Empty(t, beyond, "offset at the end returns no rows")

	// Stable order (design D6): walking in pages of 2 reproduces the unbounded
	// order exactly — no duplicate, no skip.
	var walked []uuid.UUID
	for off := 0; ; off += 2 {
		pg, err := f.svc.ListDocuments(ctx, &category, nil, nil, 2, off)
		require.NoError(t, err)
		for _, d := range pg {
			walked = append(walked, d.ID)
		}
		if len(pg) < 2 {
			break
		}
	}
	want := make([]uuid.UUID, len(all))
	for i, d := range all {
		want[i] = d.ID
	}
	require.Equal(t, want, walked, "paged walk must match the unbounded order with no dup/skip")
}
