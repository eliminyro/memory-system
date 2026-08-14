package main

import "testing"

const fixturePath = "testdata/fixture.json"

func TestLoadDataset(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}
	if len(instances) != 3 {
		t.Fatalf("got %d instances, want 3", len(instances))
	}

	wantGoldCounts := map[string]int{
		"single_session_001":       1,
		"multi_session_002":        2,
		"knowledge_update_003_abs": 0,
	}
	wantEffective := map[string]string{
		"single_session_001":       "single-session-user",
		"multi_session_002":        "multi-session",
		"knowledge_update_003_abs": "abstention",
	}

	seen := map[string]bool{}
	for _, inst := range instances {
		seen[inst.QuestionID] = true

		wantGold, ok := wantGoldCounts[inst.QuestionID]
		if !ok {
			t.Fatalf("unexpected question_id %q", inst.QuestionID)
		}
		if got := len(inst.AnswerSessionIDs); got != wantGold {
			t.Errorf("%s: %d gold sessions, want %d", inst.QuestionID, got, wantGold)
		}

		if got := EffectiveType(inst); got != wantEffective[inst.QuestionID] {
			t.Errorf("%s: EffectiveType = %q, want %q", inst.QuestionID, got, wantEffective[inst.QuestionID])
		}
	}
	for id := range wantGoldCounts {
		if !seen[id] {
			t.Errorf("fixture missing expected question_id %q", id)
		}
	}
}
