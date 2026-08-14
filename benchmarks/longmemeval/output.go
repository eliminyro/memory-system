package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// RunMeta captures a run's provenance: dataset, seed/n, embedder identity,
// commit, and timestamp — enough to reproduce or compare a run later.
type RunMeta struct {
	Dataset          string `json:"dataset"`
	Seed             int64  `json:"seed"`
	N                int    `json:"n"`
	EmbedderProvider string `json:"embedder_provider"`
	EmbedderModel    string `json:"embedder_model"`
	EmbedderDims     int    `json:"embedder_dims"`
	Commit           string `json:"commit"`
	Timestamp        string `json:"timestamp"`
	KValues          []int  `json:"k_values"`
}

// BenchmarkResults is the full machine-readable output: run metadata plus one
// Report per retrieval mode ("hybrid", "vector_only", "lexical_only").
type BenchmarkResults struct {
	Run   RunMeta           `json:"run"`
	Modes map[string]Report `json:"modes"`
}

// WriteJSON writes r as indented JSON to path.
func WriteJSON(path string, r BenchmarkResults) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// gitCommit best-effort captures the current short commit sha; any error
// (no git, not a repo, detached tooling) yields "unknown" rather than
// failing the run.
func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// modeOrder is the fixed rendering order for modes — map iteration is
// randomized, but RESULTS.md must be byte-for-byte deterministic.
var modeOrder = []string{"hybrid", "vector_only", "lexical_only"}

// RenderResultsMarkdown renders r as deterministic markdown: a run-metadata
// header, an overall table (one row per mode), and a per-question-type
// breakdown table per mode.
func RenderResultsMarkdown(r BenchmarkResults) string {
	ks := sortedKs(r.Run.KValues)
	modes := presentModes(r.Modes)

	var b strings.Builder
	writeHeader(&b, r.Run, ks)
	writeOverallTable(&b, r.Modes, modes, ks)
	writePerTypeSection(&b, r.Modes, modes, ks)
	return b.String()
}

func sortedKs(ks []int) []int {
	out := append([]int(nil), ks...)
	sort.Ints(out)
	return out
}

// presentModes returns modeOrder filtered to keys actually present in modes,
// so a run missing a mode doesn't panic or scramble the row order.
func presentModes(modes map[string]Report) []string {
	out := make([]string, 0, len(modeOrder))
	for _, m := range modeOrder {
		if _, ok := modes[m]; ok {
			out = append(out, m)
		}
	}
	return out
}

func writeHeader(b *strings.Builder, run RunMeta, ks []int) {
	fmt.Fprintf(b, "# LongMemEval Retrieval Benchmark Results\n\n")
	fmt.Fprintf(b, "- Dataset: `%s`\n", run.Dataset)
	fmt.Fprintf(b, "- Seed: %d\n", run.Seed)
	fmt.Fprintf(b, "- N: %d\n", run.N)
	fmt.Fprintf(b, "- Embedder: %s / %s (%d dims)\n", run.EmbedderProvider, run.EmbedderModel, run.EmbedderDims)
	fmt.Fprintf(b, "- Commit: `%s`\n", run.Commit)
	fmt.Fprintf(b, "- Timestamp: %s\n", run.Timestamp)
	fmt.Fprintf(b, "- K values: %s\n\n", joinInts(ks))
	fmt.Fprintf(b, "Metrics are session-level: a retrieved section's session is its owning document's slug; the ranked session list is retrieved sections deduped to first occurrence.\n\n")
}

func joinInts(ks []int) string {
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = fmt.Sprintf("%d", k)
	}
	return strings.Join(parts, ", ")
}

// metricColumns/metricValues share one column order: partial-R@k, full-R@k
// per k (ascending), then MRR@k per k — keeps header and row cells aligned.
func metricColumns(ks []int) []string {
	cols := make([]string, 0, len(ks)*3)
	for _, k := range ks {
		cols = append(cols, fmt.Sprintf("partial-R@%d", k), fmt.Sprintf("full-R@%d", k))
	}
	for _, k := range ks {
		cols = append(cols, fmt.Sprintf("MRR@%d", k))
	}
	return cols
}

func metricValues(m KMetrics, ks []int) []string {
	vals := make([]string, 0, len(ks)*3)
	for _, k := range ks {
		vals = append(vals, pct(m.PartialRecallAtK[k]), pct(m.FullRecallAtK[k]))
	}
	for _, k := range ks {
		vals = append(vals, fmt.Sprintf("%.3f", m.MeanMRRAtK[k]))
	}
	return vals
}

func pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}

func writeOverallTable(b *strings.Builder, modes map[string]Report, order []string, ks []int) {
	b.WriteString("## Overall\n\n")
	writeTableHeader(b, append([]string{"Mode", "N"}, metricColumns(ks)...))
	for _, mode := range order {
		overall := modes[mode].Overall
		row := append([]string{mode, fmt.Sprintf("%d", overall.NumQuestions)}, metricValues(overall, ks)...)
		writeTableRow(b, row)
	}
	b.WriteString("\n")
}

func writePerTypeSection(b *strings.Builder, modes map[string]Report, order []string, ks []int) {
	b.WriteString("## Per-Question-Type\n\n")
	cols := append([]string{"Type", "N"}, metricColumns(ks)...)
	for _, mode := range order {
		fmt.Fprintf(b, "### %s\n\n", mode)
		writeTableHeader(b, cols)

		byType := modes[mode].ByType
		types := make([]string, 0, len(byType))
		for t := range byType {
			types = append(types, t)
		}
		sort.Strings(types)

		for _, t := range types {
			m := byType[t]
			row := append([]string{t, fmt.Sprintf("%d", m.NumQuestions)}, metricValues(m, ks)...)
			writeTableRow(b, row)
		}
		b.WriteString("\n")
	}
}

func writeTableHeader(b *strings.Builder, cols []string) {
	writeTableRow(b, cols)
	seps := make([]string, len(cols))
	for i := range seps {
		seps[i] = "---"
	}
	writeTableRow(b, seps)
}

func writeTableRow(b *strings.Builder, cols []string) {
	b.WriteString("| " + strings.Join(cols, " | ") + " |\n")
}
