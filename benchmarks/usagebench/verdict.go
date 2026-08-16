package usagebench

import (
	"fmt"
	"math"
)

// Workload labels.
const (
	WorkloadGap   = "gap"   // engineered semantic-gap corpus (GapFraction > 0)
	WorkloadNoGap = "nogap" // control corpus (GapFraction == 0)
)

// D5 pre-registered decision rule (fixed before results exist).
const (
	VerdictNoise     = 0.2  // FP = FN level the verdict reads
	LiftThreshold    = 0.03 // min gap-workload nDCG@10 lift over the weight-0 baseline
	ControlTolerance = 0.01 // max allowed nDCG@10 degradation on the no-gap control
)

// Cell is one measured point of the sweep matrix: held-out metrics for a
// (workload, noise, weight) combination.
type Cell struct {
	Workload string  `json:"workload"`
	Noise    float64 `json:"noise"`
	Weight   float64 `json:"weight"`
	Metrics  Metrics `json:"metrics"`
}

// Verdict is the computed go/no-go outcome (design D5), derived from the numbers.
type Verdict struct {
	Decision         string  `json:"decision"` // GO | NO-GO | PENDING
	Noise            float64 `json:"noise"`
	BaselineNDCG10   float64 `json:"baseline_ndcg_10"`
	BestWeight       float64 `json:"best_weight"`
	BestWeightNDCG10 float64 `json:"best_weight_ndcg_10"`
	GapLift          float64 `json:"gap_lift"`
	ControlDelta     float64 `json:"control_delta"`
	LiftThreshold    float64 `json:"lift_threshold"`
	ControlTolerance float64 `json:"control_tolerance"`
	Rationale        string  `json:"rationale"`
}

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func findCell(cells []Cell, workload string, noise, weight float64) (Cell, bool) {
	for _, c := range cells {
		if c.Workload == workload && floatEq(c.Noise, noise) && floatEq(c.Weight, weight) {
			return c, true
		}
	}
	return Cell{}, false
}

// ComputeVerdict evaluates the D5 rule from the sweep cells. It never
// hardcodes an outcome: GO requires the gap-workload nDCG@10 lift at
// FP=FN=VerdictNoise to reach LiftThreshold over the weight-0 baseline AND the
// no-gap control at that best weight to degrade by no more than
// ControlTolerance. Missing cells yield PENDING (e.g. a not-yet-run harness).
func ComputeVerdict(cells []Cell) Verdict {
	v := Verdict{
		Decision:         "PENDING",
		Noise:            VerdictNoise,
		LiftThreshold:    LiftThreshold,
		ControlTolerance: ControlTolerance,
	}

	base, ok := findCell(cells, WorkloadGap, VerdictNoise, 0)
	if !ok {
		v.Rationale = fmt.Sprintf("no gap-workload baseline (weight 0) at noise %.2f", VerdictNoise)
		return v
	}
	v.BaselineNDCG10 = base.Metrics.NDCG10

	// Best weight = argmax nDCG@10 lift over the gap workload at this noise.
	bestFound := false
	for _, c := range cells {
		if c.Workload != WorkloadGap || !floatEq(c.Noise, VerdictNoise) || c.Weight <= 0 {
			continue
		}
		lift := c.Metrics.NDCG10 - v.BaselineNDCG10
		if !bestFound || lift > v.GapLift || (floatEq(lift, v.GapLift) && c.Weight < v.BestWeight) {
			bestFound = true
			v.GapLift = lift
			v.BestWeight = c.Weight
			v.BestWeightNDCG10 = c.Metrics.NDCG10
		}
	}
	if !bestFound {
		v.Rationale = "no weighted gap-workload cells to compare against the baseline"
		return v
	}

	// No-gap control must be measured at the same best weight and baseline.
	ctrlBase, okCB := findCell(cells, WorkloadNoGap, VerdictNoise, 0)
	ctrlBest, okCW := findCell(cells, WorkloadNoGap, VerdictNoise, v.BestWeight)
	if !okCB || !okCW {
		v.Rationale = fmt.Sprintf("missing no-gap control cell at noise %.2f (weight 0 and %.2f)", VerdictNoise, v.BestWeight)
		return v
	}
	v.ControlDelta = ctrlBest.Metrics.NDCG10 - ctrlBase.Metrics.NDCG10

	liftOK := v.GapLift >= LiftThreshold
	controlOK := v.ControlDelta >= -ControlTolerance
	if liftOK && controlOK {
		v.Decision = "GO"
	} else {
		v.Decision = "NO-GO"
	}
	v.Rationale = fmt.Sprintf(
		"gap lift %+.4f (need >= %+.2f) at weight %.2f, noise %.2f; control delta %+.4f (need >= %+.2f)",
		v.GapLift, LiftThreshold, v.BestWeight, VerdictNoise, v.ControlDelta, -ControlTolerance,
	)
	return v
}
