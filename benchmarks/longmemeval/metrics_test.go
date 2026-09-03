package main

import (
	"math"
	"testing"
)

func TestRankedSessions(t *testing.T) {
	cases := []struct {
		name    string
		results []RetrievedSection
		want    []string
	}{
		{"empty", nil, []string{}},
		{"no dupes", []RetrievedSection{{SessionID: "a"}, {SessionID: "b"}}, []string{"a", "b"}},
		{
			"dedup keeps first occurrence",
			[]RetrievedSection{{SessionID: "a"}, {SessionID: "b"}, {SessionID: "a"}, {SessionID: "c"}, {SessionID: "b"}},
			[]string{"a", "b", "c"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RankedSessions(c.results)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestPartialAndFullRecallAtK(t *testing.T) {
	cases := []struct {
		name        string
		ranked      []string
		gold        map[string]bool
		k           int
		wantPartial bool
		wantFull    bool
	}{
		{
			name:        "single gold within k",
			ranked:      []string{"s1", "s2", "s3", "s4", "s5"},
			gold:        map[string]bool{"s3": true},
			k:           3,
			wantPartial: true,
			wantFull:    true,
		},
		{
			name:        "single gold outside k",
			ranked:      []string{"s1", "s2", "s3", "s4", "s5"},
			gold:        map[string]bool{"s5": true},
			k:           3,
			wantPartial: false,
			wantFull:    false,
		},
		{
			// worked example from the design doc: 2 gold sessions, only 1 in top-k.
			name:        "two gold, one in top-k",
			ranked:      []string{"g1", "x", "y", "z"},
			gold:        map[string]bool{"g1": true, "g2": true},
			k:           2,
			wantPartial: true,
			wantFull:    false,
		},
		{
			name:        "two gold, both in top-k",
			ranked:      []string{"g1", "g2", "x"},
			gold:        map[string]bool{"g1": true, "g2": true},
			k:           3,
			wantPartial: true,
			wantFull:    true,
		},
		{
			name:        "no gold present",
			ranked:      []string{"s1", "s2"},
			gold:        map[string]bool{"gX": true},
			k:           10,
			wantPartial: false,
			wantFull:    false,
		},
		{
			// empty gold: nothing to recall, so full must be false, not vacuously true.
			name:        "empty gold",
			ranked:      []string{"s1", "s2"},
			gold:        map[string]bool{},
			k:           10,
			wantPartial: false,
			wantFull:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PartialRecallAtK(c.ranked, c.gold, c.k); got != c.wantPartial {
				t.Errorf("PartialRecallAtK = %v, want %v", got, c.wantPartial)
			}
			if got := FullRecallAtK(c.ranked, c.gold, c.k); got != c.wantFull {
				t.Errorf("FullRecallAtK = %v, want %v", got, c.wantFull)
			}
		})
	}
}

func TestReciprocalRank(t *testing.T) {
	cases := []struct {
		name   string
		ranked []string
		gold   map[string]bool
		want   float64
	}{
		// worked example from the design doc: first gold at rank 3 -> 1/3.
		{"first gold at rank 3", []string{"a", "b", "g", "d"}, map[string]bool{"g": true}, 1.0 / 3.0},
		{"first gold at rank 1", []string{"g", "a", "b"}, map[string]bool{"g": true}, 1.0},
		{"no gold present", []string{"a", "b"}, map[string]bool{"z": true}, 0},
		{"empty ranked", nil, map[string]bool{"z": true}, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReciprocalRank(c.ranked, c.gold)
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("ReciprocalRank = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAggregateOverFixture(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	// Synthetic ranked-session orders for the fixture's questions (as if
	// retrieval had run); the abstention question's gold is empty and must
	// be excluded from every aggregate below.
	rankedBySession := map[string][]string{
		"single_session_001":       {"sess_2", "sess_1"},           // gold sess_1 at rank 2 -> RR 0.5
		"multi_session_002":        {"sess_3", "sess_5", "sess_4"}, // gold sess_3@1, sess_4@3 -> RR 1.0
		"knowledge_update_003_abs": {"sess_6"},
	}

	var results []QuestionResult
	for _, inst := range instances {
		gold := make(map[string]bool, len(inst.AnswerSessionIDs))
		for _, s := range inst.AnswerSessionIDs {
			gold[s] = true
		}
		results = append(results, QuestionResult{
			QuestionID:    inst.QuestionID,
			EffectiveType: EffectiveType(inst),
			Ranked:        rankedBySession[inst.QuestionID],
			Gold:          gold,
		})
	}

	report := Aggregate(results)

	if report.Overall.NumQuestions != 2 {
		t.Fatalf("Overall.NumQuestions = %d, want 2 (empty-gold abstention excluded)", report.Overall.NumQuestions)
	}
	for _, k := range KValues {
		if got := report.Overall.PartialRecallAtK[k]; got != 1.0 {
			t.Errorf("k=%d: Overall.PartialRecallAtK = %v, want 1.0", k, got)
		}
		if got := report.Overall.FullRecallAtK[k]; got != 1.0 {
			t.Errorf("k=%d: Overall.FullRecallAtK = %v, want 1.0", k, got)
		}
		if got, want := report.Overall.MeanMRRAtK[k], 0.75; math.Abs(got-want) > 1e-9 {
			t.Errorf("k=%d: Overall.MeanMRRAtK = %v, want %v", k, got, want)
		}
	}

	if _, ok := report.ByType["abstention"]; ok {
		t.Error("abstention type present in ByType, want excluded (empty gold)")
	}
	if len(report.ByType) != 2 {
		t.Errorf("ByType has %d entries, want 2", len(report.ByType))
	}

	single := report.ByType["single-session-user"]
	if single.NumQuestions != 1 || math.Abs(single.MeanMRRAtK[5]-0.5) > 1e-9 {
		t.Errorf("single-session-user = %+v, want NumQuestions=1 MeanMRRAtK[5]=0.5", single)
	}
	multi := report.ByType["multi-session"]
	if multi.NumQuestions != 1 || math.Abs(multi.MeanMRRAtK[5]-1.0) > 1e-9 {
		t.Errorf("multi-session = %+v, want NumQuestions=1 MeanMRRAtK[5]=1.0", multi)
	}
}
