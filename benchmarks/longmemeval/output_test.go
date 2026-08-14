package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixtureResults() BenchmarkResults {
	return BenchmarkResults{
		Run: RunMeta{
			Dataset:          "testdata/fixture.json",
			Seed:             42,
			N:                3,
			EmbedderProvider: "ollama",
			EmbedderModel:    "nomic-embed-text",
			EmbedderDims:     768,
			Commit:           "abc1234",
			Timestamp:        "2026-08-13T12:00:00Z",
			KValues:          []int{10, 5}, // deliberately unsorted: renderer must sort
		},
		Modes: map[string]Report{
			// vector_only intentionally omitted: renderer must skip absent modes.
			"hybrid": {
				Overall: KMetrics{
					NumQuestions:     2,
					PartialRecallAtK: map[int]float64{5: 1, 10: 1},
					FullRecallAtK:    map[int]float64{5: 0.5, 10: 1},
					MeanMRRAtK:       map[int]float64{5: 0.75, 10: 0.8},
				},
				ByType: map[string]KMetrics{
					"multi-session": {
						NumQuestions:     1,
						PartialRecallAtK: map[int]float64{5: 1, 10: 1},
						FullRecallAtK:    map[int]float64{5: 1, 10: 1},
						MeanMRRAtK:       map[int]float64{5: 1, 10: 1},
					},
					"single-session-user": {
						NumQuestions:     1,
						PartialRecallAtK: map[int]float64{5: 1, 10: 1},
						FullRecallAtK:    map[int]float64{5: 0, 10: 1},
						MeanMRRAtK:       map[int]float64{5: 0.5, 10: 0.6},
					},
				},
			},
			"lexical_only": {
				Overall: KMetrics{
					NumQuestions:     2,
					PartialRecallAtK: map[int]float64{5: 0.5, 10: 0.5},
					FullRecallAtK:    map[int]float64{5: 0, 10: 0.5},
					MeanMRRAtK:       map[int]float64{5: 1.0 / 3.0, 10: 0.4},
				},
				ByType: map[string]KMetrics{
					"multi-session": {
						NumQuestions:     1,
						PartialRecallAtK: map[int]float64{5: 0, 10: 0},
						FullRecallAtK:    map[int]float64{5: 0, 10: 0},
						MeanMRRAtK:       map[int]float64{5: 0, 10: 0},
					},
					"single-session-user": {
						NumQuestions:     1,
						PartialRecallAtK: map[int]float64{5: 1, 10: 1},
						FullRecallAtK:    map[int]float64{5: 0, 10: 1},
						MeanMRRAtK:       map[int]float64{5: 2.0 / 3.0, 10: 0.8},
					},
				},
			},
		},
	}
}

const wantResultsMarkdown = "# LongMemEval Retrieval Benchmark Results\n" +
	"\n" +
	"- Dataset: `testdata/fixture.json`\n" +
	"- Seed: 42\n" +
	"- N: 3\n" +
	"- Embedder: ollama / nomic-embed-text (768 dims)\n" +
	"- Commit: `abc1234`\n" +
	"- Timestamp: 2026-08-13T12:00:00Z\n" +
	"- K values: 5, 10\n" +
	"\n" +
	"Metrics are session-level: a retrieved section's session is its owning document's slug; the ranked session list is retrieved sections deduped to first occurrence.\n" +
	"\n" +
	"## Overall\n" +
	"\n" +
	"| Mode | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
	"| hybrid | 2 | 100.0% | 50.0% | 100.0% | 100.0% | 0.750 | 0.800 |\n" +
	"| lexical_only | 2 | 50.0% | 0.0% | 50.0% | 50.0% | 0.333 | 0.400 |\n" +
	"\n" +
	"## Per-Question-Type\n" +
	"\n" +
	"### hybrid\n" +
	"\n" +
	"| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
	"| multi-session | 1 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |\n" +
	"| single-session-user | 1 | 100.0% | 0.0% | 100.0% | 100.0% | 0.500 | 0.600 |\n" +
	"\n" +
	"### lexical_only\n" +
	"\n" +
	"| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
	"| multi-session | 1 | 0.0% | 0.0% | 0.0% | 0.0% | 0.000 | 0.000 |\n" +
	"| single-session-user | 1 | 100.0% | 0.0% | 100.0% | 100.0% | 0.667 | 0.800 |\n" +
	"\n"

func TestRenderResultsMarkdown(t *testing.T) {
	got := RenderResultsMarkdown(fixtureResults())
	if got != wantResultsMarkdown {
		t.Errorf("RenderResultsMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantResultsMarkdown)
	}
}

func TestRenderResultsMarkdownDeterministic(t *testing.T) {
	fixture := fixtureResults()
	first := RenderResultsMarkdown(fixture)
	for i := 0; i < 5; i++ {
		if got := RenderResultsMarkdown(fixture); got != first {
			t.Fatalf("run %d: RenderResultsMarkdown not deterministic across repeated calls", i)
		}
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	want := fixtureResults()

	path := filepath.Join(t.TempDir(), "results.json")
	if err := WriteJSON(path, want); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got BenchmarkResults
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}
