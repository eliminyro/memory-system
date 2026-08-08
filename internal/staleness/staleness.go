// Package staleness decides whether a section's content is withheld: verified
// older than its doc_type threshold AND making claims that rot (code paths,
// symbols). The advisory-vs-guarded split is deliberate — "function X at Y:Z"
// claims actively mislead when stale, plain learnings don't.
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

// codePathPattern matches file paths, extensions, or file:line refs likely to
// rot with code (e.g. "internal/foo.go", ".ts", "foo.go:42").
var codePathPattern = regexp.MustCompile(
	`(?:\b(?:internal|cmd|pkg|src|app|lib|ops|bin)/[\w./-]+` +
		`|\b[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|kt|rb|php|sh|yaml|yml|tf|hcl|sql)\b` +
		`|\b[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rs):\d+)`,
)

// MentionsCodePath reports whether content references a code path likely to rot.
func MentionsCodePath(content string) bool {
	return codePathPattern.MatchString(content)
}

// ExtractVerifyHints returns up to `max` unique matched code paths for a verification hint.
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

// Preview returns a short preview of content for refused sections (default 200 chars).
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

// ThresholdStore loads and caches staleness thresholds (5-min TTL to avoid a DB hit per search).
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

// DaysFor returns the day threshold for a doc_type, falling back to the reference threshold. Cached.
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

	// Write lock before loading so concurrent misses serialise (else every
	// goroutine issues its own DB load). Double-check under the lock in case
	// another goroutine already refreshed.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && time.Since(s.cachedAt) < s.defaultTTL {
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
	s.cache = cache
	s.cachedAt = time.Now()
	return nil
}

// Invalidate forces a reload on the next lookup (for admin tools that change thresholds).
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

// ErrGuarded signals the caller must pass force_read or verify-then-mark before reading.
var ErrGuarded = errors.New("section requires verification")

// Check evaluates whether a section is guarded. NULL verified_at is treated as
// verified at creation (migration backfills legacy rows; model default handles new).
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
