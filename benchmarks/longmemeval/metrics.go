package main

// RetrievedSection is one retrieved item in rank order; SessionID is the
// owning session id (the rendered doc's slug).
type RetrievedSection struct {
	SessionID string
}

// RankedSessions dedups results to each session's first occurrence,
// preserving rank order.
func RankedSessions(results []RetrievedSection) []string {
	seen := make(map[string]struct{}, len(results))
	ranked := make([]string, 0, len(results))
	for _, r := range results {
		if _, ok := seen[r.SessionID]; ok {
			continue
		}
		seen[r.SessionID] = struct{}{}
		ranked = append(ranked, r.SessionID)
	}
	return ranked
}

// PartialRecallAtK is true iff at least one gold session is in the top-k.
func PartialRecallAtK(ranked []string, gold map[string]bool, k int) bool {
	for i, s := range ranked {
		if i >= k {
			break
		}
		if gold[s] {
			return true
		}
	}
	return false
}

// FullRecallAtK is true iff every gold session is in the top-k (false, not
// vacuously true, when gold is empty — there's nothing to fully recall).
func FullRecallAtK(ranked []string, gold map[string]bool, k int) bool {
	if len(gold) == 0 {
		return false
	}
	found := make(map[string]bool, len(gold))
	for i, s := range ranked {
		if i >= k {
			break
		}
		if gold[s] {
			found[s] = true
		}
	}
	for g := range gold {
		if !found[g] {
			return false
		}
	}
	return true
}

// ReciprocalRank returns 1/rank (1-based) of the first gold session in
// ranked, or 0 if no gold session is present.
func ReciprocalRank(ranked []string, gold map[string]bool) float64 {
	for i, s := range ranked {
		if gold[s] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// reciprocalRankAtK is ReciprocalRank restricted to the top-k of ranked, the
// standard MRR@k definition: 0 if the first gold session falls past k.
func reciprocalRankAtK(ranked []string, gold map[string]bool, k int) float64 {
	if k < len(ranked) {
		ranked = ranked[:k]
	}
	return ReciprocalRank(ranked, gold)
}

// KValues are the recall@k / MRR@k cutoffs the aggregator reports.
var KValues = []int{5, 10}

// QuestionResult is one scored question: its ranked sessions, gold session
// set, and effective type (question_type, or "abstention" for "_abs" ids).
type QuestionResult struct {
	QuestionID    string
	EffectiveType string
	Ranked        []string
	Gold          map[string]bool
}

// KMetrics holds recall@k / MRR@k stats, keyed by k, for one slice of
// questions.
type KMetrics struct {
	NumQuestions     int
	PartialRecallAtK map[int]float64
	FullRecallAtK    map[int]float64
	MeanMRRAtK       map[int]float64
}

// Report is the full aggregation: overall metrics plus a breakdown per
// EffectiveType.
type Report struct {
	Overall KMetrics
	ByType  map[string]KMetrics
}

// Aggregate computes recall@k/MRR@k over results, overall and per
// EffectiveType. Abstention questions with an empty gold set have nothing to
// recall, so they're excluded from every aggregate here entirely rather than
// scored 0 — scoring them would punish a correct "no evidence" case.
func Aggregate(results []QuestionResult) Report {
	byType := make(map[string][]QuestionResult)
	var overall []QuestionResult
	for _, r := range results {
		if len(r.Gold) == 0 {
			continue
		}
		overall = append(overall, r)
		byType[r.EffectiveType] = append(byType[r.EffectiveType], r)
	}

	report := Report{
		Overall: aggregateOne(overall),
		ByType:  make(map[string]KMetrics, len(byType)),
	}
	for t, rs := range byType {
		report.ByType[t] = aggregateOne(rs)
	}
	return report
}

func aggregateOne(results []QuestionResult) KMetrics {
	m := KMetrics{
		NumQuestions:     len(results),
		PartialRecallAtK: make(map[int]float64, len(KValues)),
		FullRecallAtK:    make(map[int]float64, len(KValues)),
		MeanMRRAtK:       make(map[int]float64, len(KValues)),
	}
	if len(results) == 0 {
		return m
	}
	n := float64(len(results))
	for _, k := range KValues {
		var partial, full int
		var mrrSum float64
		for _, r := range results {
			if PartialRecallAtK(r.Ranked, r.Gold, k) {
				partial++
			}
			if FullRecallAtK(r.Ranked, r.Gold, k) {
				full++
			}
			mrrSum += reciprocalRankAtK(r.Ranked, r.Gold, k)
		}
		m.PartialRecallAtK[k] = float64(partial) / n
		m.FullRecallAtK[k] = float64(full) / n
		m.MeanMRRAtK[k] = mrrSum / n
	}
	return m
}
