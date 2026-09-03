package main

import (
	"math/rand"
	"sort"
)

// SelectSlice returns a deterministic, seeded N-question slice of instances
// (task 5.1): sort by QuestionID for a stable base order, then seeded-shuffle
// and take the first n. Same seed+n always selects the same question set.
// n <= 0 or n >= len(instances) returns the full (sorted+shuffled) set.
func SelectSlice(instances []Instance, seed int64, n int) []Instance {
	sorted := make([]Instance, len(instances))
	copy(sorted, instances)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].QuestionID < sorted[j].QuestionID })

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(sorted), func(i, j int) { sorted[i], sorted[j] = sorted[j], sorted[i] })

	if n <= 0 || n >= len(sorted) {
		return sorted
	}
	return sorted[:n]
}
