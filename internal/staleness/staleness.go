// Package staleness decides whether a section's content should be withheld from
// a reader because its verification is older than the configured threshold for
// the parent document's type, AND the content makes claims that can go stale
// (references to code paths, symbols, or identifiers in source files).
//
// The separation between "stale but advisory" and "stale and guarded" is
// deliberate: general learnings age gracefully, but claims like "function X is
// at file Y:Z" rot and actively mislead when trusted blindly.
package staleness

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
)

// codePathPattern matches content that names files, paths, or symbols likely
// to change with code. Hits include:
//   - explicit path fragments ("internal/foo.go", "src/bar.ts")
//   - file extensions on tokens (".go", ".ts", ".py", ".rs", ".tsx", ".jsx")
//   - function/file:line references ("foo.go:42")
var codePathPattern = regexp.MustCompile(
	`(?i)(?:\b(?:internal|cmd|pkg|src|app|lib|ops|bin)/[\w./-]+` +
		`|\b[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|kt|rb|php|sh|yaml|yml|tf|hcl|sql)\b` +
		`|\b[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rs):\d+)`,
)

// MentionsCodePath returns true if content references a path or filename that
// is likely to rot when code changes.
func MentionsCodePath(content string) bool {
	return codePathPattern.MatchString(content)
}

// ExtractVerifyHints pulls the first few matched code paths out of content
// for use in a verification hint. Returns at most `max` unique matches.
func ExtractVerifyHints(content string, max int) []string {
	if max <= 0 {
		max = 5
	}
	raw := codePathPattern.FindAllString(content, -1)
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, max)
	for _, m := range raw {
		m = strings.TrimSpace(m)
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
		if len(out) >= max {
			break
		}
	}
	return out
}

// Previewer returns a short preview of content for refused sections.
// Default preview length is 200 chars.
func Preview(content string, max int) string {
	if max <= 0 {
		max = 200
	}
	content = strings.TrimSpace(content)
	if len(content) <= max {
		return content
	}
	return content[:max] + "…"
}

// ThresholdStore loads and caches staleness thresholds. Thresholds rarely
// change, so cache for 5 minutes to avoid a DB hit on every search.
type ThresholdStore struct {
	db *gorm.DB

	mu         sync.RWMutex
	cache      map[string]int
	cachedAt   time.Time
	defaultTTL time.Duration
}

// NewThresholdStore returns a threshold cache backed by `db`.
func NewThresholdStore(db *gorm.DB) *ThresholdStore {
	return &ThresholdStore{
		db:         db,
		defaultTTL: 5 * time.Minute,
	}
}

// DaysFor returns the threshold in days for a doc_type, falling back to the
// reference threshold if no row exists. Cached.
func (s *ThresholdStore) DaysFor(ctx context.Context, docType string) (int, error) {
	if err := s.refreshIfStale(ctx); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if days, ok := s.cache[docType]; ok {
		return days, nil
	}
	if days, ok := s.cache[models.DocTypeReference]; ok {
		return days, nil
	}
	return 90, nil // absolute fallback
}

func (s *ThresholdStore) refreshIfStale(ctx context.Context) error {
	s.mu.RLock()
	fresh := s.cache != nil && time.Since(s.cachedAt) < s.defaultTTL
	s.mu.RUnlock()
	if fresh {
		return nil
	}

	var rows []models.StalenessThreshold
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return fmt.Errorf("load staleness thresholds: %w", err)
	}

	cache := make(map[string]int, len(rows))
	for _, r := range rows {
		cache[r.DocType] = r.Days
	}

	s.mu.Lock()
	s.cache = cache
	s.cachedAt = time.Now()
	s.mu.Unlock()
	return nil
}

// Invalidate forces a reload on the next lookup. Exposed so admin tools that
// change thresholds can signal the store.
func (s *ThresholdStore) Invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// CheckResult describes what to do with a section relative to staleness.
type CheckResult struct {
	Stale         bool // verified_at is older than threshold (advisory)
	Guarded       bool // Stale AND mentions code path — server should refuse content
	ThresholdDays int
	Age           time.Duration
}

// ErrGuarded is returned when the caller must either pass force_read or
// verify-then-mark the section before reading. Wrap with context where needed.
var ErrGuarded = errors.New("section requires verification")

// Check evaluates whether a section is guarded. A section with NULL verified_at
// is treated as verified at creation — the migration backfills this for legacy
// rows, and the model default handles new rows.
func Check(ctx context.Context, store *ThresholdStore, section models.Section, docType string) (CheckResult, error) {
	days, err := store.DaysFor(ctx, docType)
	if err != nil {
		return CheckResult{}, err
	}

	verifiedAt := section.CreatedAt
	if section.VerifiedAt != nil {
		verifiedAt = *section.VerifiedAt
	}
	age := time.Since(verifiedAt)
	stale := age > time.Duration(days)*24*time.Hour

	result := CheckResult{ThresholdDays: days, Age: age, Stale: stale}
	if stale && MentionsCodePath(section.Content) {
		result.Guarded = true
	}
	return result, nil
}
