package repository

import (
	"testing"

	"github.com/google/uuid"
)

// TestUsageTerm_ColdStartSignAndBounds covers the closed-form adjustment: cold
// start (n=0) is exactly 0 with no div-by-zero, the term is bounded by ±weight
// for any history, and it is positive for hit-dominant / negative for
// miss-dominant records (D1).
func TestUsageTerm_ColdStartSignAndBounds(t *testing.T) {
	const w = 2.0

	if got := usageTerm(0, 0, w); got != 0 {
		t.Fatalf("cold-start term = %v, want exactly 0", got)
	}

	for _, c := range [][2]int{{1, 0}, {1000, 0}, {0, 1000}, {50, 5}, {5, 50}} {
		got := usageTerm(c[0], c[1], w)
		if got <= -w || got >= w {
			t.Fatalf("usageTerm(%d,%d,%v) = %v, want within (-w, w)", c[0], c[1], w, got)
		}
	}

	if got := usageTerm(50, 5, w); got <= 0 {
		t.Fatalf("hit-dominant term = %v, want > 0", got)
	}
	if got := usageTerm(5, 50, w); got >= 0 {
		t.Fatalf("miss-dominant term = %v, want < 0", got)
	}
}

// TestFuseHybridScored_WeightZeroIsNoOp is the default-safety gate: with recall
// counts populated, weight 0 must skip the term entirely so scores equal the
// base fused scores byte-for-byte (vec-only rows ⇒ score == VecSim, no math).
func TestFuseHybridScored_WeightZeroIsNoOp(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.8, HitCount: 40, MissCount: 1},
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.6, HitCount: 0, MissCount: 30},
	}
	out, _ := fuseHybridScored(rows, 0)
	if len(out) != 2 || out[0].Score != 0.8 || out[1].Score != 0.6 {
		t.Fatalf("weight 0 not byte-identical to base fused scores: got %+v", out)
	}
}

// TestFuseHybridScored_ColdStartNeutralAtWeight: a 0/0 candidate's score is
// unchanged even at a large weight — never-recalled docs are never touched.
func TestFuseHybridScored_ColdStartNeutralAtWeight(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.8, HitCount: 0, MissCount: 0},
	}
	out, _ := fuseHybridScored(rows, 5)
	if len(out) != 1 || out[0].Score != 0.8 {
		t.Fatalf("cold-start at weight 5: got %+v, want score 0.8 unchanged", out)
	}
}

// TestFuseHybridScored_HitsOutrankMisses: two candidates with equal base score
// differing only in recall history — at weight > 0 the hit-dominant one sorts
// strictly above the miss-dominant one (usage is the only differentiator).
func TestFuseHybridScored_HitsOutrankMisses(t *testing.T) {
	hits, misses := uuid.New(), uuid.New()
	rows := []hybridRow{
		{SectionID: misses, HasVec: true, VecSim: 0.6, HitCount: 1, MissCount: 40},
		{SectionID: hits, HasVec: true, VecSim: 0.6, HitCount: 40, MissCount: 1},
	}
	out, _ := fuseHybridScored(rows, 1)
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2", len(out))
	}
	if out[0].SectionID != hits {
		t.Fatalf("hit-dominant row must rank first, got %+v", out)
	}
	if out[0].Score <= out[1].Score {
		t.Fatalf("hit-dominant score %v must exceed miss-dominant %v", out[0].Score, out[1].Score)
	}
}

// TestFuseHybridScored_TierFromBaseScore documents the Tier choice: usage
// re-orders survivors but never re-tiers them. A large positive boost lifts the
// adjusted Score yet the tier stays "standard" (calibrated from base 0.65).
func TestFuseHybridScored_TierFromBaseScore(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.65, HitCount: 1000, MissCount: 0},
	}
	out, _ := fuseHybridScored(rows, 5)
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	if out[0].Tier != "standard" {
		t.Fatalf("tier = %q, want standard (from base score, not usage-boosted)", out[0].Tier)
	}
	if out[0].Score <= 0.65 {
		t.Fatalf("adjusted score %v should reflect the positive usage boost", out[0].Score)
	}
}
