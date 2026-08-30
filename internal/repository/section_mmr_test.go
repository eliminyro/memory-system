package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// vec2D builds a fixture embedding with only two components set — enough to
// control cosine similarity exactly for the tests below.
func vec2D(c0, c1 float32) pgvector.Vector {
	return pgvector.NewVector([]float32{c0, c1})
}

// TestMMR_DefaultAndEscapeHatchMatchPreChangeFuse is the safety gate (task 3.1):
// the MMRLambda==nil path (fuseHybrid itself — the exact call HybridSearch's
// nil branch makes) and the lambda>=0.999 escape hatch (via applyMMR) must
// both produce the same order+membership fuseHybrid already produced before
// this change, on a fixture mixing vec-only, lex-only, both, and a sub-floor row.
func TestMMR_DefaultAndEscapeHatchMatchPreChangeFuse(t *testing.T) {
	rows := []hybridRow{
		{SectionID: uuid.New(), Content: "vec-only-high", HasVec: true, VecSim: 0.95, Embedding: vec2D(1, 0)},
		{SectionID: uuid.New(), Content: "both", HasVec: true, VecSim: 0.6, HasLex: true, LexRank: 1.0, Embedding: vec2D(0.9, 0.1)},
		{SectionID: uuid.New(), Content: "lex-only", HasLex: true, LexRank: 0.9, Embedding: vec2D(0.1, 0.9)},
		{SectionID: uuid.New(), Content: "below-floor", HasVec: true, VecSim: 0.2, Embedding: vec2D(0, 1)},
	}
	limit := 10

	want := fuseHybrid(rows, limit, 20, 0, nil) // RRF reference order; below-floor row gated
	wantOrder := []string{"both", "vec-only-high", "lex-only"}
	for i, w := range wantOrder {
		if want[i].Content != w {
			t.Fatalf("reference order sanity check failed at %d: got %q, want %q", i, want[i].Content, w)
		}
	}

	// (a) MMRLambda == nil: HybridSearch's nil branch is `return fuseHybrid(rows, p.Limit), nil` verbatim.
	gotDefault := fuseHybrid(rows, limit, 20, 0, nil)
	assertSameSectionOrder(t, want, gotDefault)

	// (b) MMRLambda == 0.999 escape hatch via applyMMR.
	scored, embs := fuseHybridScored(rows, 20, 0, nil)
	gotEscape := applyMMR(scored, embs, 0.999, limit, 20)
	assertSameSectionOrder(t, want, gotEscape)
}

func assertSameSectionOrder(t *testing.T, want, got []SearchResult) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("length mismatch: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if want[i].SectionID != got[i].SectionID {
			t.Fatalf("position %d: want %s, got %s", i, want[i].SectionID, got[i].SectionID)
		}
	}
}

// TestApplyMMR_NearDuplicateDisplacedAtLimit: near-dup top scorers A, B (cosine
// ~0.99997), diverse relevant C (cosine(A,C) ~0.649), low anchor D setting the
// min-max floor so C keeps real relevance (F4). limit 2, lambda 0.5 => A + C, not B.
func TestApplyMMR_NearDuplicateDisplacedAtLimit(t *testing.T) {
	a := SearchResult{SectionID: uuid.New(), Score: 0.99}
	b := SearchResult{SectionID: uuid.New(), Score: 0.98}
	c := SearchResult{SectionID: uuid.New(), Score: 0.90}
	d := SearchResult{SectionID: uuid.New(), Score: 0.30}
	scored := []SearchResult{a, b, c, d} // already score-sorted desc, as fuseHybridScored guarantees
	embs := []pgvector.Vector{
		vec2D(0.99, 0.14107),   // A
		vec2D(0.989, 0.14791),  // B: cosine(A,B) ~ 0.99997 -- near-duplicate of A
		vec2D(0.75, -0.661438), // C: cosine(A,C) ~ 0.649 -- diverse from A/B
		vec2D(-0.5, -0.866),    // D: diverse low-score anchor (pool min)
	}

	got := applyMMR(scored, embs, 0.5, 2, 20)

	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	present := map[uuid.UUID]bool{got[0].SectionID: true, got[1].SectionID: true}
	if !present[a.SectionID] {
		t.Fatalf("expected top-relevance A in the result, got %v", got)
	}
	if !present[c.SectionID] || present[b.SectionID] {
		t.Fatalf("expected diverse C to displace near-duplicate B, got %v", got)
	}
}

// TestApplyMMR_MembershipPreservedCountMinNL confirms MMR changes order, not
// membership: every returned result is a pool member, results are unique, and
// count = min(N, L) across a range of limits including "no limit" (<=0).
func TestApplyMMR_MembershipPreservedCountMinNL(t *testing.T) {
	scored := make([]SearchResult, 5)
	embs := make([]pgvector.Vector, 5)
	pool := map[uuid.UUID]bool{}
	for i := range scored {
		id := uuid.New()
		scored[i] = SearchResult{SectionID: id, Score: 1.0 - float64(i)*0.05}
		embs[i] = vec2D(float32(i)+1, 0)
		pool[id] = true
	}

	for _, limit := range []int{0, 3, 5, 10} {
		got := applyMMR(scored, embs, 0.7, limit, 20)
		want := len(scored)
		if limit > 0 && limit < want {
			want = limit
		}
		if len(got) != want {
			t.Fatalf("limit=%d: got %d results, want %d", limit, len(got), want)
		}
		seen := map[uuid.UUID]bool{}
		for _, r := range got {
			if !pool[r.SectionID] {
				t.Fatalf("limit=%d: fabricated result %s not in pool", limit, r.SectionID)
			}
			if seen[r.SectionID] {
				t.Fatalf("limit=%d: duplicate result %s", limit, r.SectionID)
			}
			seen[r.SectionID] = true
		}
	}
}

// TestCosineSimilarity covers the zero-norm and dimension-mismatch guards:
// both must yield 0, never a panic or NaN, since lexical-only candidates can
// carry a zero-vector fallback (see HybridSearch's '[0]'::vector COALESCE).
func TestCosineSimilarity(t *testing.T) {
	unit := vec2D(1, 0)
	orthogonal := vec2D(0, 1)
	zero := pgvector.NewVector([]float32{0, 0})
	mismatched := pgvector.NewVector([]float32{1, 0, 0})

	if got := cosineSimilarity(unit, unit); got != 1 {
		t.Errorf("identical unit vectors: got %v, want 1", got)
	}
	if got := cosineSimilarity(unit, orthogonal); got != 0 {
		t.Errorf("orthogonal vectors: got %v, want 0", got)
	}
	if got := cosineSimilarity(unit, zero); got != 0 {
		t.Errorf("zero vector: got %v, want 0", got)
	}
	if got := cosineSimilarity(unit, mismatched); got != 0 {
		t.Errorf("dimension mismatch: got %v, want 0", got)
	}
}
