package usagebench

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRecallAtK(t *testing.T) {
	ranked := []string{"a", "x", "b", "y", "c"}
	gold := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if got := recallAtK(ranked, gold, 3); !approx(got, 2.0/4.0) {
		t.Fatalf("recall@3 = %v, want 0.5", got)
	}
	if got := recallAtK(ranked, gold, 5); !approx(got, 3.0/4.0) {
		t.Fatalf("recall@5 = %v, want 0.75", got)
	}
	if got := recallAtK(ranked, map[string]bool{}, 5); got != 0 {
		t.Fatalf("recall with empty gold = %v, want 0", got)
	}
}

func TestReciprocalRank(t *testing.T) {
	gold := map[string]bool{"b": true}
	if got := reciprocalRank([]string{"a", "b", "c"}, gold); !approx(got, 0.5) {
		t.Fatalf("MRR = %v, want 0.5", got)
	}
	if got := reciprocalRank([]string{"a", "c"}, gold); got != 0 {
		t.Fatalf("MRR with no gold = %v, want 0", got)
	}
}

func TestNDCGAtK(t *testing.T) {
	// gold at ranks 1 and 3; DCG = 1/log2(2) + 1/log2(4) = 1.5.
	// IDCG (2 relevant) = 1/log2(2) + 1/log2(3) ≈ 1.63093.
	ranked := []string{"g1", "x", "g2", "y"}
	gold := map[string]bool{"g1": true, "g2": true}
	want := (1.0 + 0.5) / (1.0 + 1.0/math.Log2(3))
	if got := ndcgAtK(ranked, gold, 10); !approx(got, want) {
		t.Fatalf("nDCG@10 = %v, want %v", got, want)
	}
	// Perfect ranking ⇒ 1.0.
	if got := ndcgAtK([]string{"g1", "g2", "z"}, gold, 10); !approx(got, 1.0) {
		t.Fatalf("perfect nDCG = %v, want 1.0", got)
	}
}

func TestEvaluate(t *testing.T) {
	// gold = {a,b}; ranked puts a at rank 1, b at rank 3.
	m := evaluate([]string{"a", "x", "b"}, []string{"a", "b"})
	if !approx(m.RAt5, 1.0) {
		t.Fatalf("R@5 = %v, want 1.0", m.RAt5)
	}
	if !approx(m.MRR, 1.0) {
		t.Fatalf("MRR = %v, want 1.0", m.MRR)
	}
	want := (1.0 + 0.5) / (1.0 + 1.0/math.Log2(3))
	if !approx(m.NDCG10, want) {
		t.Fatalf("nDCG@10 = %v, want %v", m.NDCG10, want)
	}
}

func TestMeanMetrics(t *testing.T) {
	got := meanMetrics([]Metrics{
		{RAt5: 0.4, RAt10: 0.6, MRR: 1.0, NDCG10: 0.8},
		{RAt5: 0.6, RAt10: 0.8, MRR: 0.5, NDCG10: 0.6},
	})
	if !approx(got.RAt5, 0.5) || !approx(got.RAt10, 0.7) || !approx(got.MRR, 0.75) || !approx(got.NDCG10, 0.7) {
		t.Fatalf("mean = %+v", got)
	}
	if got := meanMetrics(nil); got != (Metrics{}) {
		t.Fatalf("mean of empty = %+v, want zero", got)
	}
}
