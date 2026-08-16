package usagebench

import "testing"

// cellsFor builds a minimal matrix at VerdictNoise with the given nDCG@10s.
func cellsFor(gap, nogap map[float64]float64) []Cell {
	var cells []Cell
	for w, v := range gap {
		cells = append(cells, Cell{Workload: WorkloadGap, Noise: VerdictNoise, Weight: w, Metrics: Metrics{NDCG10: v}})
	}
	for w, v := range nogap {
		cells = append(cells, Cell{Workload: WorkloadNoGap, Noise: VerdictNoise, Weight: w, Metrics: Metrics{NDCG10: v}})
	}
	return cells
}

func TestVerdict_GO(t *testing.T) {
	cells := cellsFor(
		map[float64]float64{0: 0.80, 0.5: 0.88, 1: 0.90},
		map[float64]float64{0: 0.99, 0.5: 0.99, 1: 0.99},
	)
	v := ComputeVerdict(cells)
	if v.Decision != "GO" {
		t.Fatalf("decision = %s, want GO (%s)", v.Decision, v.Rationale)
	}
	if v.BestWeight != 1 {
		t.Fatalf("best weight = %v, want 1", v.BestWeight)
	}
	if !approx(v.GapLift, 0.10) {
		t.Fatalf("gap lift = %v, want 0.10", v.GapLift)
	}
}

func TestVerdict_NoGo_LiftTooSmall(t *testing.T) {
	cells := cellsFor(
		map[float64]float64{0: 0.80, 1: 0.81},
		map[float64]float64{0: 0.99, 1: 0.99},
	)
	if v := ComputeVerdict(cells); v.Decision != "NO-GO" {
		t.Fatalf("decision = %s, want NO-GO (%s)", v.Decision, v.Rationale)
	}
}

func TestVerdict_NoGo_ControlHurts(t *testing.T) {
	cells := cellsFor(
		map[float64]float64{0: 0.80, 1: 0.90}, // lift 0.10 passes
		map[float64]float64{0: 0.99, 1: 0.90}, // control drops 0.09 > tolerance
	)
	if v := ComputeVerdict(cells); v.Decision != "NO-GO" {
		t.Fatalf("decision = %s, want NO-GO (%s)", v.Decision, v.Rationale)
	}
}

func TestVerdict_Pending_MissingCells(t *testing.T) {
	// No baseline gap cell at all.
	if v := ComputeVerdict(nil); v.Decision != "PENDING" {
		t.Fatalf("decision = %s, want PENDING", v.Decision)
	}
	// Gap present but control missing.
	cells := cellsFor(map[float64]float64{0: 0.80, 1: 0.90}, nil)
	if v := ComputeVerdict(cells); v.Decision != "PENDING" {
		t.Fatalf("decision = %s, want PENDING (%s)", v.Decision, v.Rationale)
	}
}
