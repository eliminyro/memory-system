//go:build integration

package service_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// snippetCap mirrors the service default (WithSnippetChars unset in the fixture).
const snippetCap = 400

// hasSentinel reports whether text still carries either PUA highlight rune.
func hasSentinel(s string) bool {
	return strings.Contains(s, repository.SnippetStartSel) || strings.Contains(s, repository.SnippetStopSel)
}

// findSection runs the search and returns the result for secID, retrying to
// absorb the rare under-return of unique-token hits (see PerTenantStaleness).
func findSection(t *testing.T, f *authzFixture, ctx context.Context, query string, secID uuid.UUID, snippet bool) repository.SearchResult {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		results, _, err := f.svc.Search(ctx, query, nil, nil, 20, false, "", nil, snippet)
		require.NoError(t, err)
		for _, r := range results {
			if r.SectionID == secID {
				return r
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("section %s not found in search results", secID)
	return repository.SearchResult{}
}

// TestSearchSnippet_OmittedPreservesFullContent (6.1): snippet=false yields the
// full section body byte-identical to storage and no snippet_centered flag.
func TestSearchSnippet_OmittedPreservesFullContent(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "snip" + uuid.NewString()[:8]
	body := strings.Repeat("alpha beta gamma delta ", 40) + " " + token + " tail"

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "snip-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	var sec models.Section
	require.NoError(t, f.db.Where("id = ?", secID).First(&sec).Error)

	r := findSection(t, f, ctx, token, secID, false)
	require.Equal(t, sec.Content, r.Content, "snippet=false returns full content unchanged")
	require.Nil(t, r.SnippetCentered, "snippet_centered absent when snippet mode is off")
}

// TestSearchSnippet_LexicalHitIsCentered (6.2): snippet=true on a lexical hit
// returns a <=cap window containing the matched term, centered=true, no sentinel.
func TestSearchSnippet_LexicalHitIsCentered(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "snip" + uuid.NewString()[:8]
	// Match sits deep in the body so a leading-text window would not contain it.
	body := strings.Repeat("alpha beta gamma delta epsilon zeta ", 30) + " " + token + " " + strings.Repeat("omega ", 30)

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "snip-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	var sec models.Section
	require.NoError(t, f.db.Where("id = ?", secID).First(&sec).Error)

	r := findSection(t, f, ctx, token, secID, true)
	require.NotNil(t, r.SnippetCentered)
	require.True(t, *r.SnippetCentered, "lexical hit is centered")
	require.Contains(t, r.Content, token, "window contains the matched term")
	require.False(t, hasSentinel(r.Content), "returned text carries no PUA sentinel")
	require.LessOrEqual(t, utf8.RuneCountInString(r.Content), snippetCap, "window hard-trimmed to cap")
	require.Less(t, len(r.Content), len(sec.Content), "window is shorter than the full body")
}

// TestSearchSnippet_CenteredWindowSurvivesLongLeadingTokens is the regression for
// the match-aware trim: long leading tokens make ts_headline's word-window exceed
// the char cap, so a front-truncation would drop the tail where the match sits.
// The centered window must still contain the matched term.
func TestSearchSnippet_CenteredWindowSurvivesLongLeadingTokens(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "snip" + uuid.NewString()[:8]
	// Long single-lexeme filler well past the cap, THEN the match near the end.
	lead := strings.Repeat("longleadingfillertokenaaaaaaaaaa ", 80) // ~2600 chars
	body := lead + " " + token + " trailing text here"

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "snip-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	r := findSection(t, f, ctx, token, secID, true)
	require.NotNil(t, r.SnippetCentered)
	require.True(t, *r.SnippetCentered, "lexical hit is centered")
	require.Contains(t, r.Content, token, "centered window keeps the matched term despite long leading tokens")
	require.LessOrEqual(t, utf8.RuneCountInString(r.Content), snippetCap, "window respects the char cap")
	require.False(t, hasSentinel(r.Content), "returned text carries no PUA sentinel")
}

// TestSearchSnippet_SemanticOnlyHitNotCentered (6.3): a purely-semantic hit (no
// shared lexeme; embedding forced to match the query) falls back to leading text
// with centered=false and no sentinel.
func TestSearchSnippet_SemanticOnlyHitNotCentered(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "sem" + uuid.NewString()[:8] // absent from the body -> no lexical match
	body := strings.Repeat("unrelated filler words here ", 40)

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "sem-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	secID := res.Document.Sections[0].ID

	// Force a semantic-only match: set the section embedding equal to the query's
	// (deterministic FakeEmbedder) so vec_sim = 1.0 while no lexeme is shared.
	emb, err := service.NewFakeEmbedder(fakeDim).Embed(context.Background(), token)
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&models.Section{}).Where("id = ?", secID).Update("embedding", emb).Error)

	r := findSection(t, f, ctx, token, secID, true)
	require.NotNil(t, r.SnippetCentered)
	require.False(t, *r.SnippetCentered, "purely-semantic hit falls back to leading text")
	require.NotEmpty(t, r.Content, "leading-text window is non-empty")
	require.False(t, hasSentinel(r.Content), "returned text carries no PUA sentinel")
	require.NotContains(t, r.Content, token, "the query term is not in the body")
}

// TestSearchSnippet_WithheldResultNotExpanded (6.4, load-bearing): on a hard-mode
// tenant a withheld result keeps blanked content + preview + hints under
// snippet=true, while a sibling off-mode result in the same response is windowed.
func TestSearchSnippet_WithheldResultNotExpanded(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	// A = hard, B = off.
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantB).
		Update("staleness_mode", models.StalenessModeOff).Error)

	token := "snip" + uuid.NewString()[:8]
	// Mentions a code path -> guard-eligible once stale.
	body := "internal/service/memory.go is where it lives " + token
	resA, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil,
		"sa-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	resB, err := f.svc.StoreDocument(ctxFor(f.tenantB, f.subjB), "learnings", nil,
		"sb-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)

	secA := resA.Document.Sections[0].ID
	secB := resB.Document.Sections[0].ID
	old := time.Now().Add(-400 * 24 * time.Hour)
	require.NoError(t, f.db.Model(&models.Section{}).
		Where("id IN ?", []uuid.UUID{secA, secB}).Update("verified_at", old).Error)

	var ra, rb repository.SearchResult
	var okA, okB bool
	for attempt := 0; attempt < 5; attempt++ {
		results, _, err := f.svc.Search(ctxFor(f.tenantA, f.subjA), token, nil, nil, 20, false, "", nil, true)
		require.NoError(t, err)
		bySection := map[uuid.UUID]repository.SearchResult{}
		for _, r := range results {
			bySection[r.SectionID] = r
		}
		ra, okA = bySection[secA]
		rb, okB = bySection[secB]
		if okA && okB {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	require.True(t, okA, "hard-mode tenant section present")
	require.True(t, okB, "off-mode tenant section present")

	// Withheld result: blanked content + preview + hints, never snippet-expanded.
	require.Equal(t, "needs_verification", ra.Status)
	require.Empty(t, ra.Content, "withheld content is not snippet-expanded")
	require.Nil(t, ra.SnippetCentered, "withheld result carries no snippet flag")
	require.NotEmpty(t, ra.Preview, "withheld result keeps its preview")
	require.NotEmpty(t, ra.VerifyHints, "withheld result keeps its verify hints")

	// Sibling non-withheld result is windowed normally.
	require.NotEmpty(t, rb.Content, "off-mode sibling is windowed, not blanked")
	require.False(t, hasSentinel(rb.Content), "windowed sibling carries no sentinel")
	require.NotNil(t, rb.SnippetCentered)
	require.True(t, *rb.SnippetCentered, "sibling matched the token -> centered")
}

// TestSearchSnippet_GetDocumentStillFull (6.5): get_document on a snippet-returned
// doc yields full, unabridged content.
func TestSearchSnippet_GetDocumentStillFull(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	token := "snip" + uuid.NewString()[:8]
	body := strings.Repeat("alpha beta gamma delta epsilon ", 40) + " " + token + " tail"

	res, err := f.svc.StoreDocument(ctx, "learnings", nil, "snip-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	doc := res.Document
	secID := doc.Sections[0].ID

	var sec models.Section
	require.NoError(t, f.db.Where("id = ?", secID).First(&sec).Error)

	// Snippet search windows the content...
	r := findSection(t, f, ctx, token, secID, true)
	require.Less(t, len(r.Content), len(sec.Content), "search snippet is windowed")

	// ...but get_document returns the full body untouched.
	view, err := f.svc.GetDocument(ctx, doc.Category, nil, doc.Slug, false, "", nil)
	require.NoError(t, err)
	var got string
	for _, sv := range view.Sections {
		if sv.ID == secID {
			got = sv.Content
		}
	}
	require.Equal(t, sec.Content, got, "get_document yields full content")
	require.False(t, hasSentinel(got), "full content carries no sentinel")
}
