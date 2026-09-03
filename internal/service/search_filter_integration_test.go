//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// searchUntil runs a search and returns the results once want is present,
// retrying to absorb the rare under-return of unique-token hits (the fake
// embedder ties every vector, see snippet_integration_test's findSection).
func searchUntil(t *testing.T, svc *service.MemoryService, ctx context.Context, query string, docType *string, want uuid.UUID) []repository.SearchResult {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		results, err := svc.Search(ctx, query, nil, nil, docType, 20, false, "", nil, false)
		require.NoError(t, err)
		for _, r := range results {
			if r.SectionID == want {
				return results
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("section %s not found in search results", want)
	return nil
}

// TestSearch_DocTypeFilter (5.3): a doc_type-scoped search returns only sections
// whose owning document has that type; a nil filter is unfiltered; an unknown
// type matches nothing. This also guards the SQL arg alignment (design D4).
func TestSearch_DocTypeFilter(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "dtype" + uuid.NewString()[:8]

	resPS, err := f.svc.StoreDocument(ctx, "learnings", nil, "ps-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" body one", true, "seed", nil, nil)
	require.NoError(t, err)
	resLe, err := f.svc.StoreDocument(ctx, "learnings", nil, "le-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" body two", true, "seed", nil, nil)
	require.NoError(t, err)

	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", resPS.Document.ID).
		Update("doc_type", models.DocTypeProjectState).Error)
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", resLe.Document.ID).
		Update("doc_type", models.DocTypeLearning).Error)

	secPS := resPS.Document.Sections[0].ID
	secLe := resLe.Document.Sections[0].ID

	// Filtered to project_state: only that type surfaces, learning excluded.
	ps := models.DocTypeProjectState
	filtered := searchUntil(t, f.svc, ctx, token, &ps, secPS)
	var sawLe bool
	for _, r := range filtered {
		require.Equal(t, models.DocTypeProjectState, r.DocType, "doc_type filter must exclude other types")
		if r.SectionID == secLe {
			sawLe = true
		}
	}
	require.False(t, sawLe, "learning section excluded under project_state filter")

	// Unfiltered: both types present.
	all := searchUntil(t, f.svc, ctx, token, nil, secPS)
	byID := map[uuid.UUID]bool{}
	for _, r := range all {
		byID[r.SectionID] = true
	}
	require.True(t, byID[secPS], "nil doc_type includes the project_state section")
	require.True(t, byID[secLe], "nil doc_type includes the learning section")

	// Unknown type matches nothing, without erroring.
	unknown := "nonexistent_type"
	none, err := f.svc.Search(ctx, token, nil, nil, &unknown, 20, false, "", nil, false)
	require.NoError(t, err)
	require.Empty(t, none, "unknown doc_type returns no results")
}

// TestSearch_CandidatePoolBoundsCandidates (5.4): a non-default pool changes the
// effective per-list candidate LIMIT — pool=1 caps each list to a single row, so
// far fewer candidates survive than at the default pool of 20.
func TestSearch_CandidatePoolBoundsCandidates(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "pool" + uuid.NewString()[:8]

	const n = 5
	secs := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		res, err := f.svc.StoreDocument(ctx, "learnings", nil, "pool-"+uuid.NewString(),
			"# T\n\n## H\n"+token+" body", true, "seed", nil, nil)
		require.NoError(t, err)
		secs = append(secs, res.Document.Sections[0].ID)
	}

	// Default pool (20): every token doc is a both-list match, so all surface.
	var defaultResults []repository.SearchResult
	for attempt := 0; attempt < 5; attempt++ {
		r, err := f.svc.Search(ctx, token, nil, nil, nil, 20, false, "", nil, false)
		require.NoError(t, err)
		found := map[uuid.UUID]bool{}
		for _, x := range r {
			found[x.SectionID] = true
		}
		complete := true
		for _, s := range secs {
			if !found[s] {
				complete = false
			}
		}
		if complete {
			defaultResults = r
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(defaultResults), n, "default pool surfaces all token matches")

	// Pool of 1: each CTE LIMITs to one row, so at most two fused candidates.
	poolSvc := service.NewMemoryService(
		f.db,
		repository.NewDocumentRepository(f.db),
		repository.NewSectionRepository(f.db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(f.db),
		repository.NewAPIKeyRepository(f.db),
		repository.NewLintRepository(f.db),
		staleness.NewPolicyStore(f.db),
		repository.NewOverrideLogRepository(f.db),
		repository.NewCleanupQueueRepository(f.db),
		nil, nil,
		f.store,
		service.WithCandidatePool(1),
	)
	poolResults, err := poolSvc.Search(ctx, token, nil, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	require.LessOrEqual(t, len(poolResults), 2, "pool=1 caps each candidate list to one row")
	require.Less(t, len(poolResults), len(defaultResults), "a smaller pool yields fewer candidates")
}
