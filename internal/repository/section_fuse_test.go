package repository

import (
	"testing"

	"github.com/google/uuid"
)

// TestFuseHybrid_LexicalCoMatchIsBoostNotPenalty is the B3 regression. A row that
// matches BOTH signals must never score below what its vector match alone would
// give. Before the fix a weak normalized lexical rank dragged a strong vector
// match down (0.4*vec + 0.6*lex) and could push it under scoreFloor, dropping a
// genuinely relevant result.
func TestFuseHybrid_LexicalCoMatchIsBoostNotPenalty(t *testing.T) {
	target := uuid.New()
	rows := []hybridRow{
		// Sets maxLex = 1.0 so the target's LexRank normalizes to a weak 0.1.
		{SectionID: uuid.New(), DocumentID: uuid.New(), HasLex: true, LexRank: 1.0},
		// Strong vector (0.5) + weak lexical co-match. Old score: 0.4*0.5 + 0.6*0.1
		// = 0.26 < scoreFloor(0.4) ⇒ dropped. Fixed: max(0.5, 0.26) = 0.5 ⇒ kept.
		{SectionID: target, DocumentID: uuid.New(), HasVec: true, VecSim: 0.5, HasLex: true, LexRank: 0.1},
	}

	out := fuseHybrid(rows, 0, 10)

	var got *SearchResult
	for i := range out {
		if out[i].SectionID == target {
			got = &out[i]
		}
	}
	if got == nil {
		t.Fatal("target dropped: a weak lexical co-match must not push a strong vector match below the score floor")
	}
	if got.Score != 0.5 {
		t.Fatalf("target score = %v, want 0.5 (max(vec, fused))", got.Score)
	}
	if got.Tier != "standard" {
		t.Fatalf("target tier = %q, want standard", got.Tier)
	}
}

// TestFuseHybrid_StrongLexicalStillBoosts confirms the max() keeps the boost
// direction: when the normalized lexical rank is high, the fused score exceeds
// the vector-only score.
func TestFuseHybrid_StrongLexicalStillBoosts(t *testing.T) {
	target := uuid.New()
	rows := []hybridRow{
		{SectionID: target, DocumentID: uuid.New(), HasVec: true, VecSim: 0.5, HasLex: true, LexRank: 1.0},
	}
	out := fuseHybrid(rows, 0, 10)
	if len(out) != 1 {
		t.Fatalf("got %d results, want 1", len(out))
	}
	// maxLex = 1.0 ⇒ lex = 1.0 ⇒ fused = 0.4*0.5 + 0.6*1.0 = 0.8 > vec 0.5.
	if out[0].Score != 0.8 {
		t.Fatalf("score = %v, want 0.8 (lexical boost above vec-only 0.5)", out[0].Score)
	}
	if out[0].Tier != "high" {
		t.Fatalf("tier = %q, want high", out[0].Tier)
	}
}
