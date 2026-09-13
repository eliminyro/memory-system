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

// archiveFlagged flags every section of a doc and archives it (the archive-on-
// grace end state), so the cold/dormant path has something to surface.
func archiveFlagged(t *testing.T, f *authzFixture, docID uuid.UUID) {
	t.Helper()
	require.NoError(t, f.db.Model(&models.Section{}).Where("document_id = ?", docID).
		Updates(map[string]any{"flagged_at": time.Now(), "flag_reason": "stale"}).Error)
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", docID).
		Updates(map[string]any{"archived_at": time.Now(), "archive_reason": models.ArchiveReasonStale}).Error)
}

// flagOnly flags a doc's sections without archiving (still in the hot pool).
func flagOnly(t *testing.T, f *authzFixture, docID uuid.UUID) {
	t.Helper()
	require.NoError(t, f.db.Model(&models.Section{}).Where("document_id = ?", docID).
		Updates(map[string]any{"flagged_at": time.Now(), "flag_reason": "stale"}).Error)
}

// fallbackSvc builds a service over the fixture's db/store with an explicit cold-
// pass threshold, so a test can force (high) or disable (0) the dormant fallback.
func fallbackSvc(f *authzFixture, threshold int) *service.MemoryService {
	return service.NewMemoryService(
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
		service.WithFallbackThreshold(threshold),
	)
}

// TestDormant_RichHotExcludesArchived (§11): with the default threshold and a rich
// hot result, no archived doc surfaces and nothing is labeled dormant.
func TestDormant_RichHotExcludesArchived(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "rich" + uuid.NewString()[:8]

	arch, err := f.svc.StoreDocument(ctx, "learnings", nil, "arch-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" archived body", true, "seed", nil, nil)
	require.NoError(t, err)
	archiveFlagged(t, f, arch.Document.ID)

	var live uuid.UUID
	for i := 0; i < 4; i++ {
		res, err := f.svc.StoreDocument(ctx, "learnings", nil, "live-"+uuid.NewString(),
			"# T\n\n## H\n"+token+" live body", true, "seed", nil, nil)
		require.NoError(t, err)
		live = res.Document.Sections[0].ID
	}

	// The hot pool answers richly (>= threshold), so the cold pass never runs.
	results := searchUntil(t, f.svc, ctx, token, nil, live)
	for _, r := range results {
		require.NotEqual(t, arch.Document.ID, r.DocumentID, "archived doc must not appear in a rich hot result")
		require.False(t, r.Dormant, "no hit is dormant when the cold pass is skipped")
	}
}

// TestDormant_ThinHotAppendsDormantThenRevive (§11): a thin hot result appends the
// archived match labeled dormant (after the hot hits); re-verifying it unarchives
// the doc so a later query ranks it hot, not dormant.
func TestDormant_ThinHotAppendsDormantThenRevive(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	svc := fallbackSvc(f, 1000) // force the cold pass whenever hot is thin
	token := "thin" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "dorm-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" dormant body", true, "seed", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID
	secID := res.Document.Sections[0].ID
	archiveFlagged(t, f, docID)

	got := findDocInSearch(t, svc, ctx, token, docID)
	require.True(t, got.Dormant, "archived match is labeled dormant on a thin hot result")
	requireDormantLast(t, mustSearch(t, svc, ctx, token))

	// Re-verify the surfaced dormant doc: it unarchives (rejoins the hot pool).
	require.NoError(t, svc.MarkVerified(ctx, secID, nil))
	var revived models.Document
	require.NoError(t, f.db.First(&revived, "id = ?", docID).Error)
	require.Nil(t, revived.ArchivedAt, "verifying a dormant doc unarchives it")

	after := findDocInSearch(t, svc, ctx, token, docID)
	require.False(t, after.Dormant, "the revived doc ranks hot, not dormant")
}

// TestDormant_TierAllUnchanged (§11): Tier=all (and the empty default) reproduce
// the pre-tier behaviour — archived excluded; hot excludes, cold includes only.
func TestDormant_TierAllUnchanged(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "tier" + uuid.NewString()[:8]

	liveRes, err := f.svc.StoreDocument(ctx, "learnings", nil, "tl-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" live body", true, "seed", nil, nil)
	require.NoError(t, err)
	archRes, err := f.svc.StoreDocument(ctx, "learnings", nil, "ta-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" archived body", true, "seed", nil, nil)
	require.NoError(t, err)
	archiveFlagged(t, f, archRes.Document.ID)

	liveSec := liveRes.Document.Sections[0].ID
	archSec := archRes.Document.Sections[0].ID

	repo := repository.NewSectionRepository(f.db)
	emb, err := service.NewFakeEmbedder(fakeDim).Embed(ctx, token)
	require.NoError(t, err)
	base := repository.SearchParams{TenantIDs: []uuid.UUID{f.tenantA}, Embedding: emb, Query: token, Limit: 20}

	all, err := repo.HybridSearch(ctx, base) // empty Tier == TierAll: archived excluded
	require.NoError(t, err)
	require.True(t, hasSection(all, liveSec), "Tier=all keeps the live section")
	require.False(t, hasSection(all, archSec), "Tier=all excludes the archived section (unchanged)")

	base.Tier = repository.TierCold
	cold, err := repo.HybridSearch(ctx, base)
	require.NoError(t, err)
	require.True(t, hasSection(cold, archSec), "Tier=cold returns the archived section")
	require.False(t, hasSection(cold, liveSec), "Tier=cold excludes the live section")

	base.Tier = repository.TierHot
	hot, err := repo.HybridSearch(ctx, base)
	require.NoError(t, err)
	require.True(t, hasSection(hot, liveSec), "Tier=hot keeps the live section")
	require.False(t, hasSection(hot, archSec), "Tier=hot excludes the archived section")
}

// TestDormant_FlaggedCarriesArchivesInDays (§11): a flagged, non-prunable doc's
// search result and list item both carry the advisory archives_in_days warning.
func TestDormant_FlaggedCarriesArchivesInDays(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "warn" + uuid.NewString()[:8]

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "warn-"+uuid.NewString(),
		"# T\n\n## H\n"+token+" flagged body", true, "seed", nil, nil)
	require.NoError(t, err)
	docID := res.Document.ID
	secID := res.Document.Sections[0].ID
	flagOnly(t, f, docID)

	results := searchUntil(t, f.svc, ctx, token, nil, secID)
	var hit *repository.SearchResult
	for i := range results {
		if results[i].SectionID == secID {
			hit = &results[i]
		}
	}
	require.NotNil(t, hit)
	require.Equal(t, "needs_verification", hit.Status)
	require.NotNil(t, hit.ArchivesInDays, "a flagged non-prunable search hit carries archives_in_days")

	cat := "learnings"
	docs, err := f.svc.ListDocuments(ctx, &cat, nil, nil, service.ListOptions{})
	require.NoError(t, err)
	var listed *models.Document
	for i := range docs {
		if docs[i].ID == docID {
			listed = &docs[i]
		}
	}
	require.NotNil(t, listed)
	require.True(t, listed.Flagged, "a flagged doc is marked flagged in the list")
	require.NotNil(t, listed.ArchivesInDays, "a flagged non-prunable list item carries archives_in_days")
}

// --- small local helpers ---

func mustSearch(t *testing.T, svc *service.MemoryService, ctx context.Context, token string) []repository.SearchResult {
	t.Helper()
	results, err := svc.Search(ctx, token, nil, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	return results
}

// findDocInSearch polls Search until a result for docID appears, returning it.
func findDocInSearch(t *testing.T, svc *service.MemoryService, ctx context.Context, token string, docID uuid.UUID) repository.SearchResult {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		for _, r := range mustSearch(t, svc, ctx, token) {
			if r.DocumentID == docID {
				return r
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("document %s not found in search results", docID)
	return repository.SearchResult{}
}

// requireDormantLast asserts every dormant hit sorts after every hot hit.
func requireDormantLast(t *testing.T, results []repository.SearchResult) {
	t.Helper()
	sawDormant := false
	for _, r := range results {
		if r.Dormant {
			sawDormant = true
			continue
		}
		require.False(t, sawDormant, "a hot hit must never follow a dormant one")
	}
}

func hasSection(results []repository.SearchResult, id uuid.UUID) bool {
	for _, r := range results {
		if r.SectionID == id {
			return true
		}
	}
	return false
}
