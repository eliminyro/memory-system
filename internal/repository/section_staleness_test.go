package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestStalenessFactor covers the pure penalty shape: identity within threshold or
// when disabled, monotonic decay past threshold, and a clamped floor at 1-weight
// for age >= 2x the threshold.
func TestStalenessFactor(t *testing.T) {
	const th = 30
	const w = 0.5

	if got := stalenessFactor(10, th, w); got != 1.0 {
		t.Errorf("age within threshold: got %v, want 1.0", got)
	}
	if got := stalenessFactor(th, th, w); got != 1.0 {
		t.Errorf("age == threshold: got %v, want 1.0", got)
	}
	if got := stalenessFactor(100, th, 0); got != 1.0 {
		t.Errorf("weight 0: got %v, want 1.0", got)
	}
	if got := stalenessFactor(100, 0, w); got != 1.0 {
		t.Errorf("threshold 0: got %v, want 1.0", got)
	}

	if got := stalenessFactor(45, th, w); got != 0.75 {
		t.Errorf("midway ramp: got %v, want 0.75", got)
	}
	if got := stalenessFactor(2*th, th, w); got != 1-w {
		t.Errorf("age 2x threshold: got %v, want %v (floor)", got, 1-w)
	}
	if got := stalenessFactor(1000, th, w); got != 1-w {
		t.Errorf("age far past: got %v, want %v (clamped floor)", got, 1-w)
	}

	// Monotonic non-increasing as age grows, never below the floor.
	prev := 1.1
	for age := th; age <= 3*th; age++ {
		got := stalenessFactor(age, th, w)
		if got > prev {
			t.Fatalf("not monotonic at age %d: %v > %v", age, got, prev)
		}
		if got < 1-w {
			t.Fatalf("below floor at age %d: %v < %v", age, got, 1-w)
		}
		prev = got
	}
}

// TestFuse_StalePenaltyReordersEquallyRelevant: two candidates with identical fused
// scores (each rank 1 in one list, rank 2 in the other), stale sorting first in the
// baseline; the penalty drops it below fresh, and off it holds baseline order.
func TestFuse_StalePenaltyReordersEquallyRelevant(t *testing.T) {
	staleID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	freshID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	now := time.Now()
	fresh := now.Add(-5 * 24 * time.Hour)
	stale := now.Add(-100 * 24 * time.Hour)
	thresholds := map[string]int{"reference": 30}

	// fresh: vecRank 1 + lexRank 2; stale: vecRank 2 + lexRank 1 => equal scores.
	rows := []hybridRow{
		{SectionID: freshID, DocType: "reference", VerifiedAt: &fresh, HasVec: true, VecSim: 0.9, HasLex: true, LexRank: 0.5},
		{SectionID: staleID, DocType: "reference", VerifiedAt: &stale, HasVec: true, VecSim: 0.8, HasLex: true, LexRank: 0.9},
	}

	base := fuseHybrid(rows, 10, 20, 0, nil)
	if len(base) != 2 || base[0].SectionID != staleID {
		t.Fatalf("baseline: want stale first (equal scores, smaller id), got %v", contents(base))
	}
	if base[0].Score != base[1].Score {
		t.Fatalf("baseline candidates must be equally relevant: %.6f vs %.6f", base[0].Score, base[1].Score)
	}

	on := fuseHybrid(rows, 10, 20, 0.5, thresholds)
	f, s := findByID(on, freshID), findByID(on, staleID)
	if f == nil || s == nil {
		t.Fatal("penalty must reorder, never remove a candidate")
	}
	if on[0].SectionID != freshID {
		t.Fatalf("with penalty on: fresh must rank first, got %v", contents(on))
	}
	if s.Score >= f.Score {
		t.Fatalf("stale score %.6f must fall below fresh %.6f", s.Score, f.Score)
	}

	// Weight 0 and empty thresholds each fall back to the baseline order exactly.
	zero := fuseHybrid(rows, 10, 20, 0, thresholds)
	empty := fuseHybrid(rows, 10, 20, 0.5, nil)
	for _, got := range [][]SearchResult{zero, empty} {
		if got[0].SectionID != base[0].SectionID || got[1].SectionID != base[1].SectionID {
			t.Fatalf("no-penalty path must match baseline order, got %v", contents(got))
		}
	}
}
