package repository

import (
	"testing"

	"github.com/google/uuid"
)

func TestFuseHybrid(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), Content: "both", HasVec: true, VecSim: 0.8, HasLex: true, LexRank: 1.0},
		{SectionID: uuid.New(), Content: "vec-only", HasVec: true, VecSim: 0.6},
		{SectionID: uuid.New(), Content: "lex-only", HasLex: true, LexRank: 0.9},
		{SectionID: uuid.New(), Content: "weak-vec", HasVec: true, VecSim: 0.3},
	}

	got := fuseHybrid(rows, 10, 20, 0, nil)

	// weak-vec is vector-only below vecOnlyFloor ⇒ gated out.
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3 (weak-vec gated below vecOnlyFloor)", len(got))
	}

	byContent := map[string]SearchResult{}
	for _, r := range got {
		byContent[r.Content] = r
	}

	// RRF: both-list (vecRank 1 + lexRank 1) outscores either single-list match.
	if got[0].Content != "both" {
		t.Errorf("top result = %q, want both (order=%v)", got[0].Content, contents(got))
	}
	if r := byContent["both"]; r.Tier != "high" {
		t.Errorf("both tier = %q, want high (score %.5f)", r.Tier, r.Score)
	}
	// Lexical-only recall: a row with no vector hit must still surface, ranked
	// by its lexical contribution and tiered as a single-list top-half match.
	r, ok := byContent["lex-only"]
	if !ok {
		t.Fatal("lexical-only row was dropped; FULL OUTER JOIN recall is broken")
	}
	if r.Tier != "standard" {
		t.Errorf("lex-only tier = %q, want standard (score %.5f)", r.Tier, r.Score)
	}
}

func TestFuseHybridLimit(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), Content: "a", HasVec: true, VecSim: 0.9},
		{SectionID: uuid.New(), Content: "b", HasVec: true, VecSim: 0.8},
		{SectionID: uuid.New(), Content: "c", HasVec: true, VecSim: 0.7},
	}
	got := fuseHybrid(rows, 2, 20, 0, nil)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (limit)", len(got))
	}
	if got[0].Content != "a" || got[1].Content != "b" {
		t.Errorf("got top-2 %v, want [a b]", contents(got))
	}
}

func contents(rs []SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Content
	}
	return out
}
