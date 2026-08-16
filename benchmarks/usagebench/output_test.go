package usagebench

import (
	"strings"
	"testing"
)

func sampleResults(status string) Results {
	cells := cellsFor(
		map[float64]float64{0: 0.80, 1: 0.90},
		map[float64]float64{0: 0.99, 1: 0.99},
	)
	return Results{
		Run: RunMeta{
			Status:           status,
			Seed:             42,
			Dim:              768,
			EmbedderProvider: "fake",
			Weights:          []float64{0, 1},
			Noises:           []float64{0.2},
			Commit:           "deadbeef",
		},
		Params:  DefaultParams(42, 0.4),
		Cells:   cells,
		Verdict: ComputeVerdict(cells),
	}
}

func TestRenderMarkdown_Deterministic(t *testing.T) {
	r := sampleResults("complete")
	first := RenderMarkdown(r)
	if first != RenderMarkdown(r) {
		t.Fatal("markdown render is not deterministic")
	}
	if !strings.Contains(first, "**GO**") {
		t.Fatalf("expected GO verdict in report:\n%s", first)
	}
	if !strings.Contains(first, "gap workload — nDCG@10") {
		t.Fatal("expected gap workload table")
	}
}

func TestGitCommit(t *testing.T) {
	// Exercises the helper the runner uses; returns "unknown" outside a repo.
	if gitCommit() == "" {
		t.Fatal("gitCommit returned empty string")
	}
}

func TestRenderMarkdown_NotYetRunBanner(t *testing.T) {
	md := RenderMarkdown(sampleResults("not_yet_run"))
	if !strings.Contains(md, "STATUS: NOT YET RUN") {
		t.Fatalf("expected not-yet-run banner:\n%s", md)
	}
}
