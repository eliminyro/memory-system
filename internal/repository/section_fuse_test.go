package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// findByID returns the fused result with the given SectionID, or nil.
func findByID(rs []SearchResult, id uuid.UUID) *SearchResult {
	for i := range rs {
		if rs[i].SectionID == id {
			return &rs[i]
		}
	}
	return nil
}

// TestFuse_BothListOutranksSingle: a both-list match beats a single-list match
// purely by summing a contribution per list — no guard clause. Here the both-list
// row is even worse-ranked in the shared vector list yet still wins.
func TestFuse_BothListOutranksSingle(t *testing.T) {
	both := uuid.New()
	single := uuid.New()
	rows := []hybridRow{
		{SectionID: single, HasVec: true, VecSim: 0.9},                           // vecRank 1
		{SectionID: both, HasVec: true, VecSim: 0.8, HasLex: true, LexRank: 0.9}, // vecRank 2 + lexRank 1
	}

	out := fuseHybrid(rows, 10, 20, 0, nil)
	b, s := findByID(out, both), findByID(out, single)
	if b == nil || s == nil {
		t.Fatal("expected both candidates retained")
	}
	if b.Score <= s.Score {
		t.Fatalf("both-list score %.5f must exceed single-list score %.5f", b.Score, s.Score)
	}
	if out[0].SectionID != both {
		t.Fatal("both-list candidate must sort first")
	}
}

// TestFuse_LexicalOnlyRetained: a candidate matching only the keyword list must
// survive fusion (the point of the FULL OUTER JOIN).
func TestFuse_LexicalOnlyRetained(t *testing.T) {
	lex := uuid.New()
	out := fuseHybrid([]hybridRow{{SectionID: lex, HasLex: true, LexRank: 0.5}}, 10, 20, 0, nil)
	if findByID(out, lex) == nil {
		t.Fatal("lexical-only candidate must survive fusion")
	}
}

// TestFuse_VectorOnlyFloorGate: a distant vector-only neighbour below the floor
// is dropped, but the same low similarity is kept when the row also matches
// lexically (which has already cleared a real text match).
func TestFuse_VectorOnlyFloorGate(t *testing.T) {
	distant := uuid.New()
	rescued := uuid.New()
	rows := []hybridRow{
		{SectionID: distant, HasVec: true, VecSim: vecOnlyFloor - 0.1},
		{SectionID: rescued, HasVec: true, VecSim: vecOnlyFloor - 0.1, HasLex: true, LexRank: 0.5},
	}

	out := fuseHybrid(rows, 10, 20, 0, nil)
	if findByID(out, distant) != nil {
		t.Error("distant vector-only neighbour below vecOnlyFloor must be dropped")
	}
	if findByID(out, rescued) == nil {
		t.Error("below-floor candidate that also matches lexically must be kept")
	}
}

// TestFuse_RankContributionStableAcrossBatches: the same rank yields the same
// contribution regardless of the other results' raw scores or the batch — rank,
// not magnitude, drives fusion.
func TestFuse_RankContributionStableAcrossBatches(t *testing.T) {
	target := uuid.New()
	batch1 := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.9},
		{SectionID: target, HasVec: true, VecSim: 0.8}, // vecRank 2
	}
	batch2 := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.51},
		{SectionID: target, HasVec: true, VecSim: 0.5}, // vecRank 2, different neighbours
	}

	s1 := findByID(fuseHybrid(batch1, 10, 20, 0, nil), target)
	s2 := findByID(fuseHybrid(batch2, 10, 20, 0, nil), target)
	if s1 == nil || s2 == nil {
		t.Fatal("target missing from a batch")
	}
	if s1.Score != s2.Score {
		t.Fatalf("same rank must give same RRF contribution: %.6f vs %.6f", s1.Score, s2.Score)
	}
}

// TestFuse_TierFromStructure: a both-list match reaches "high"; a single-list
// match past the half-pool boundary falls to "low".
func TestFuse_TierFromStructure(t *testing.T) {
	const poolSize = 20
	both := uuid.New()
	deep := uuid.New()
	rows := []hybridRow{
		{SectionID: both, HasVec: true, VecSim: 0.9, HasLex: true, LexRank: 0.9},
		{SectionID: deep, HasVec: true, VecSim: vecOnlyFloor}, // survives the gate, ranks last
	}
	// Fill the vector list past the half-pool boundary so `deep` lands beyond rank pool/2.
	for i := 0; i < poolSize/2; i++ {
		rows = append(rows, hybridRow{SectionID: uuid.New(), HasVec: true, VecSim: 0.8 - float64(i)*0.01})
	}

	out := fuseHybrid(rows, 100, poolSize, 0, nil)
	b, d := findByID(out, both), findByID(out, deep)
	if b == nil || d == nil {
		t.Fatal("both/deep missing")
	}
	if b.Tier != "high" {
		t.Errorf("both-list tier = %q, want high", b.Tier)
	}
	if d.Tier != "low" {
		t.Errorf("deep single-list tier = %q, want low", d.Tier)
	}
}

// TestFusionTier_BoundaryTracksPoolSize: the single-list top-half cut scales with
// poolSize — a rank-40 single-list row is "standard" at pool 100 (within 50) but
// "low" at pool 20 (past 10).
func TestFusionTier_BoundaryTracksPoolSize(t *testing.T) {
	single := hybridRow{HasVec: true}
	if tier := fusionTier(single, 40, 0, 100); tier != "standard" {
		t.Errorf("rank 40 at pool 100 tier = %q, want standard", tier)
	}
	if tier := fusionTier(single, 40, 0, 20); tier != "low" {
		t.Errorf("rank 40 at pool 20 tier = %q, want low", tier)
	}
}

// TestFuse_ResultsAndEmbeddingsAligned: results[i] and embs[i] stay paired, and
// a gated vector-only row is dropped from both.
func TestFuse_ResultsAndEmbeddingsAligned(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.9, HasLex: true, LexRank: 0.9, Embedding: vec2D(1, 0)},
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.7, Embedding: vec2D(0, 1)},
		{SectionID: uuid.New(), HasLex: true, LexRank: 0.5, Embedding: vec2D(1, 1)},
		{SectionID: uuid.New(), HasVec: true, VecSim: 0.2, Embedding: vec2D(0.5, 0.5)}, // gated
	}

	results, embs := fuseHybridScored(rows, 20, 0, nil)
	if len(results) != len(embs) {
		t.Fatalf("results (%d) and embeddings (%d) length mismatch", len(results), len(embs))
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (one vector-only row gated)", len(results))
	}

	want := map[uuid.UUID]pgvector.Vector{
		rows[0].SectionID: rows[0].Embedding,
		rows[1].SectionID: rows[1].Embedding,
		rows[2].SectionID: rows[2].Embedding,
	}
	for i, r := range results {
		if want[r.SectionID].String() != embs[i].String() {
			t.Errorf("position %d: embedding %v not aligned with result %s", i, embs[i], r.SectionID)
		}
	}
}
