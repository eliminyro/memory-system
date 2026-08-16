package usagebench

import "math"

// Metrics are the held-out retrieval metrics for one query, or their mean over
// a query slice. Relevance is binary topic membership.
type Metrics struct {
	RAt5   float64 `json:"r_at_5"`
	RAt10  float64 `json:"r_at_10"`
	MRR    float64 `json:"mrr"`
	NDCG10 float64 `json:"ndcg_10"`
}

// recallAtK is |top-k ∩ gold| / |gold| (0 when gold is empty).
func recallAtK(ranked []string, gold map[string]bool, k int) float64 {
	if len(gold) == 0 {
		return 0
	}
	hit := 0
	for i, id := range ranked {
		if i >= k {
			break
		}
		if gold[id] {
			hit++
		}
	}
	return float64(hit) / float64(len(gold))
}

// reciprocalRank is 1/rank of the first gold item (0 if none retrieved).
func reciprocalRank(ranked []string, gold map[string]bool) float64 {
	for i, id := range ranked {
		if gold[id] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// ndcgAtK is DCG@k / IDCG@k with binary gains (0 when gold is empty).
func ndcgAtK(ranked []string, gold map[string]bool, k int) float64 {
	var dcg float64
	for i, id := range ranked {
		if i >= k {
			break
		}
		if gold[id] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	ideal := len(gold)
	if ideal > k {
		ideal = k
	}
	var idcg float64
	for i := 0; i < ideal; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// evaluate computes the four metrics for one ranked result list against gold.
func evaluate(ranked []string, goldIDs []string) Metrics {
	gold := make(map[string]bool, len(goldIDs))
	for _, id := range goldIDs {
		gold[id] = true
	}
	return Metrics{
		RAt5:   recallAtK(ranked, gold, 5),
		RAt10:  recallAtK(ranked, gold, 10),
		MRR:    reciprocalRank(ranked, gold),
		NDCG10: ndcgAtK(ranked, gold, 10),
	}
}

// meanMetrics averages per-query metrics (zero value for an empty slice).
func meanMetrics(ms []Metrics) Metrics {
	if len(ms) == 0 {
		return Metrics{}
	}
	var sum Metrics
	for _, m := range ms {
		sum.RAt5 += m.RAt5
		sum.RAt10 += m.RAt10
		sum.MRR += m.MRR
		sum.NDCG10 += m.NDCG10
	}
	n := float64(len(ms))
	return Metrics{
		RAt5:   sum.RAt5 / n,
		RAt10:  sum.RAt10 / n,
		MRR:    sum.MRR / n,
		NDCG10: sum.NDCG10 / n,
	}
}
