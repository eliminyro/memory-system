package repository

import (
	"testing"

	"github.com/google/uuid"
)

func TestRelevanceTier(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.95, "high"},
		{0.70, "high"},
		{0.69, "standard"},
		{0.50, "standard"},
		{0.49, "low"},
		{0.40, "low"},
	}
	for _, c := range cases {
		if got := relevanceTier(c.score); got != c.want {
			t.Errorf("relevanceTier(%.2f) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestFuseHybrid(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), Content: "both", HasVec: true, VecSim: 0.8, HasLex: true, LexRank: 1.0},
		{SectionID: uuid.New(), Content: "vec-only", HasVec: true, VecSim: 0.6},
		{SectionID: uuid.New(), Content: "lex-only", HasLex: true, LexRank: 0.9},
		{SectionID: uuid.New(), Content: "weak-vec", HasVec: true, VecSim: 0.3},
	}

	got := fuseHybrid(rows, 10)

	// "weak-vec" (0.3) is below the floor and must be dropped.
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3 (weak-vec should be floored out)", len(got))
	}

	// Expected fused scores (maxLex = 1.0):
	//   both     = 0.4*0.8 + 0.6*(1.0/1.0) = 0.92  -> high
	//   vec-only = 0.6                              -> standard
	//   lex-only = 0.6*(0.9/1.0)            = 0.54  -> standard
	// Sorted descending: both, vec-only, lex-only.
	wantOrder := []string{"both", "vec-only", "lex-only"}
	for i, w := range wantOrder {
		if got[i].Content != w {
			t.Errorf("position %d: got %q, want %q (order=%v)", i, got[i].Content, w, contents(got))
		}
	}

	byContent := map[string]SearchResult{}
	for _, r := range got {
		byContent[r.Content] = r
	}
	if r := byContent["both"]; r.Tier != "high" {
		t.Errorf("both tier = %q, want high (score %.3f)", r.Tier, r.Score)
	}
	if r := byContent["lex-only"]; r.Tier != "standard" {
		t.Errorf("lex-only tier = %q, want standard (score %.3f)", r.Tier, r.Score)
	}
	// Lexical-only recall: a row with no vector hit must still surface.
	if _, ok := byContent["lex-only"]; !ok {
		t.Error("lexical-only row was dropped; FULL OUTER JOIN recall is broken")
	}
}

func TestFuseHybridLimit(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), Content: "a", HasVec: true, VecSim: 0.9},
		{SectionID: uuid.New(), Content: "b", HasVec: true, VecSim: 0.8},
		{SectionID: uuid.New(), Content: "c", HasVec: true, VecSim: 0.7},
	}
	got := fuseHybrid(rows, 2)
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
