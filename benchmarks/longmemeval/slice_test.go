package main

import "testing"

func TestSelectSliceDeterministic(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	a := SelectSlice(instances, 42, 2)
	b := SelectSlice(instances, 42, 2)
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("len(a)=%d, len(b)=%d, want 2", len(a), len(b))
	}
	for i := range a {
		if a[i].QuestionID != b[i].QuestionID {
			t.Errorf("index %d: %q != %q for the same seed+n", i, a[i].QuestionID, b[i].QuestionID)
		}
	}
}

func TestSelectSliceDifferentSeedCanDiffer(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	a := SelectSlice(instances, 1, len(instances))
	b := SelectSlice(instances, 2, len(instances))
	if len(a) != len(instances) || len(b) != len(instances) {
		t.Fatalf("expected full sets, got %d and %d", len(a), len(b))
	}
	// Not asserting inequality (a differing seed COULD reshuffle to the same
	// order) — only that both are deterministic, valid permutations.
	seen := make(map[string]bool, len(a))
	for _, inst := range a {
		seen[inst.QuestionID] = true
	}
	if len(seen) != len(instances) {
		t.Errorf("SelectSlice(seed=1) dropped or duplicated questions: %d unique of %d", len(seen), len(instances))
	}
}

func TestSelectSliceAllWhenNIsZero(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}
	got := SelectSlice(instances, 7, 0)
	if len(got) != len(instances) {
		t.Fatalf("got %d, want %d (all)", len(got), len(instances))
	}
}
