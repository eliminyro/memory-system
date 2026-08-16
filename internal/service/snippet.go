package service

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/repository"
)

// containsSentinel reports whether a raw ts_headline result wrapped a match —
// i.e. the window is centered on a real lexeme, not the leading-text fallback.
func containsSentinel(s string) bool {
	return strings.Contains(s, repository.SnippetStartSel) || strings.Contains(s, repository.SnippetStopSel)
}

// stripSentinels removes both PUA highlight runes, leaving clean verbatim text.
func stripSentinels(s string) string {
	s = strings.ReplaceAll(s, repository.SnippetStartSel, "")
	return strings.ReplaceAll(s, repository.SnippetStopSel, "")
}

// trimToRunes hard-caps s to n runes on a rune boundary (never mid-rune).
func trimToRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// snippetWindow returns the cleaned, <=chars-rune snippet from a raw ts_headline
// result. Centered case (sentinel present): slice a chars-rune window centered on
// the first match sentinel BEFORE stripping, so long leading tokens can't push the
// match out of the cap. Fallback case (no sentinel): keep the leading window.
func snippetWindow(raw string, chars int) string {
	if chars <= 0 {
		return ""
	}
	start := strings.Index(raw, repository.SnippetStartSel)
	if start < 0 {
		return trimToRunes(stripSentinels(raw), chars) // leading-text fallback
	}
	runes := []rune(raw)
	matchIdx := utf8.RuneCountInString(raw[:start])
	lo := matchIdx - chars/2
	if lo < 0 {
		lo = 0
	}
	hi := lo + chars
	if hi > len(runes) {
		hi = len(runes)
		if lo = hi - chars; lo < 0 {
			lo = 0
		}
	}
	// Stripping only removes sentinels, so the result stays <= chars runes.
	return trimToRunes(stripSentinels(string(runes[lo:hi])), chars)
}

// applySnippets rewrites each non-withheld result's Content with a match-centered
// ts_headline window and sets SnippetCentered. Withheld results (Content=="") are
// skipped, so their blanked body is never reconstructed. Best-effort: a query
// error leaves results as full content (the snippet=false shape), never a failed
// search or a leak. tenantIDs is the request's readable scope (defense-in-depth).
func (s *MemoryService) applySnippets(ctx context.Context, results []repository.SearchResult, query string, tenantIDs []uuid.UUID) {
	if s.sections == nil {
		return
	}
	ids := make([]uuid.UUID, 0, len(results))
	for i := range results {
		if results[i].Content != "" {
			ids = append(ids, results[i].SectionID)
		}
	}
	if len(ids) == 0 {
		return
	}
	snips, err := s.sections.Snippets(ctx, tenantIDs, query, ids, s.snippetChars)
	if err != nil {
		slog.Default().Warn("snippet fetch failed; returning full content", "error", err)
		return
	}
	for i := range results {
		raw, ok := snips[results[i].SectionID]
		if !ok {
			continue // withheld or absent — leave untouched
		}
		centered := containsSentinel(raw)
		results[i].Content = snippetWindow(raw, s.snippetChars)
		results[i].SnippetCentered = &centered
	}
}
