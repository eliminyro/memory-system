package usagebench

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunMeta captures how a run was produced (design: results must name their inputs).
type RunMeta struct {
	Status           string    `json:"status"` // "complete" | "not_yet_run"
	Seed             int64     `json:"seed"`
	Dim              int       `json:"dim"`
	EmbedderProvider string    `json:"embedder_provider"`
	Weights          []float64 `json:"weights"`
	Noises           []float64 `json:"noises"`
	Commit           string    `json:"commit"`
	Timestamp        string    `json:"timestamp"`
}

// Results is the full machine-readable benchmark output.
type Results struct {
	Run     RunMeta   `json:"run"`
	Params  GenParams `json:"gap_workload_params"`
	Cells   []Cell    `json:"cells"`
	Verdict Verdict   `json:"verdict"`
}

// WriteJSON writes r as indented JSON to path.
func WriteJSON(path string, r Results) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteMarkdown renders r and writes RESULTS.md to path.
func WriteMarkdown(path string, r Results) error {
	if err := os.WriteFile(path, []byte(RenderMarkdown(r)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// gitCommit returns the short HEAD sha, or "unknown".
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func cellNDCG(cells []Cell, workload string, noise, weight float64) (float64, bool) {
	c, ok := findCell(cells, workload, noise, weight)
	if !ok {
		return 0, false
	}
	return c.Metrics.NDCG10, true
}

func cellR10(cells []Cell, workload string, noise, weight float64) (float64, bool) {
	c, ok := findCell(cells, workload, noise, weight)
	if !ok {
		return 0, false
	}
	return c.Metrics.RAt10, true
}

// RenderMarkdown produces a deterministic human-readable report.
func RenderMarkdown(r Results) string {
	var b strings.Builder
	b.WriteString("# Usage-weighted ranking benchmark — results\n\n")

	if r.Run.Status != "complete" {
		b.WriteString("> **STATUS: NOT YET RUN.** This file is a placeholder committed with the\n")
		b.WriteString("> harness. The matrix and verdict below are populated by section 4 of the\n")
		b.WriteString("> `phase-b-usage-benchmark` change, running the harness against a disposable\n")
		b.WriteString("> Postgres. Numbers here are absent, not results.\n\n")
	}

	fmt.Fprintf(&b, "- Embedder: `%s` (deterministic), dim %d\n", r.Run.EmbedderProvider, r.Run.Dim)
	fmt.Fprintf(&b, "- Seed: %d\n", r.Run.Seed)
	fmt.Fprintf(&b, "- Commit: `%s`\n", r.Run.Commit)
	fmt.Fprintf(&b, "- Weights: %s\n", joinFloats(r.Run.Weights))
	fmt.Fprintf(&b, "- Noise (FP=FN): %s\n\n", joinFloats(r.Run.Noises))

	b.WriteString("## Verdict\n\n")
	fmt.Fprintf(&b, "**%s** — %s\n\n", r.Verdict.Decision, r.Verdict.Rationale)
	fmt.Fprintf(&b, "- gap baseline (weight 0) nDCG@10: %s\n", fmtOrNA(r.Verdict.BaselineNDCG10, r.Verdict.Decision != "PENDING"))
	fmt.Fprintf(&b, "- best weight: %.2f, nDCG@10: %s\n", r.Verdict.BestWeight, fmtOrNA(r.Verdict.BestWeightNDCG10, r.Verdict.Decision != "PENDING"))
	fmt.Fprintf(&b, "- gap lift: %s (threshold >= +%.2f)\n", fmtOrNA(r.Verdict.GapLift, r.Verdict.Decision != "PENDING"), LiftThreshold)
	fmt.Fprintf(&b, "- no-gap control delta: %s (tolerance >= -%.2f)\n\n", fmtOrNA(r.Verdict.ControlDelta, r.Verdict.Decision != "PENDING"), ControlTolerance)

	for _, wl := range []string{WorkloadGap, WorkloadNoGap} {
		fmt.Fprintf(&b, "## %s workload — nDCG@10 (rows: weight, cols: noise)\n\n", wl)
		writeMatrix(&b, r, wl, cellNDCG)
		fmt.Fprintf(&b, "\n## %s workload — Recall@10 (rows: weight, cols: noise)\n\n", wl)
		writeMatrix(&b, r, wl, cellR10)
		b.WriteString("\n")
	}
	return b.String()
}

func writeMatrix(b *strings.Builder, r Results, workload string, get func([]Cell, string, float64, float64) (float64, bool)) {
	header := []string{"weight"}
	for _, n := range r.Run.Noises {
		header = append(header, fmt.Sprintf("noise=%.2f", n))
	}
	writeRow(b, header)
	sep := make([]string, len(header))
	for i := range sep {
		sep[i] = "---"
	}
	writeRow(b, sep)
	for _, w := range r.Run.Weights {
		row := []string{fmt.Sprintf("%.2f", w)}
		for _, n := range r.Run.Noises {
			if v, ok := get(r.Cells, workload, n, w); ok {
				row = append(row, fmt.Sprintf("%.4f", v))
			} else {
				row = append(row, "—")
			}
		}
		writeRow(b, row)
	}
}

func writeRow(b *strings.Builder, cols []string) {
	b.WriteString("| ")
	b.WriteString(strings.Join(cols, " | "))
	b.WriteString(" |\n")
}

func joinFloats(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return strings.Join(parts, ", ")
}

func fmtOrNA(v float64, present bool) string {
	if !present {
		return "n/a"
	}
	return fmt.Sprintf("%+.4f", v)
}
