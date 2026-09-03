package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// ScalePoint is one sweep measurement: the per-subcategory distractor count, the
// resulting total live bench-corpus document count, and the per-mode reports.
type ScalePoint struct {
	DistractorsPerSubcat int               `json:"distractors_per_subcat"`
	CorpusDocs           int64             `json:"corpus_docs"`
	Modes                map[string]Report `json:"modes"`
}

// ScaleSweepResults is the machine-readable sweep output: run provenance, the
// distractor levels swept, and one ScalePoint per level (baseline included).
type ScaleSweepResults struct {
	Run    RunMeta      `json:"run"`
	Sizes  []int        `json:"distractor_sizes"`
	Points []ScalePoint `json:"points"`
}

// parseScaleSizes parses "--scale-sweep" (e.g. "100,500,2000") into an ascending,
// de-duplicated, positive-int slice; empty/unset yields nil (no sweep).
func parseScaleSizes(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	seen := make(map[int]struct{}, len(parts))
	sizes := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid --scale-sweep value %q: want a positive integer", p)
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		sizes = append(sizes, v)
	}
	if len(sizes) == 0 {
		return nil, fmt.Errorf("--scale-sweep must list at least one positive integer")
	}
	sort.Ints(sizes)
	return sizes, nil
}

// distractorWords is a neutral filler vocabulary for synthetic distractors —
// generic enough that a distractor never semantically matches a gold query.
var distractorWords = []string{
	"logistics", "inventory", "quarterly", "spreadsheet", "hardware", "shipment",
	"invoice", "warehouse", "vendor", "calibration", "maintenance", "procedure",
	"schedule", "compliance", "throughput", "allocation", "baseline", "checklist",
	"routine", "metric", "protocol", "asset", "workflow", "register",
}

// distractorContent renders deterministic filler markdown for one distractor,
// seeded by subcat+idx so re-runs are byte-stable and it matches no gold query.
func distractorContent(subcat string, idx int) string {
	seed := int64(idx)*1_000_003 + 1
	for _, r := range subcat {
		seed = seed*131 + int64(r)
	}
	rng := rand.New(rand.NewSource(seed))
	var b strings.Builder
	b.WriteString("## turn 1 (user)\n")
	for s := 0; s < 3; s++ {
		for w := 0; w < 12; w++ {
			b.WriteString(distractorWords[rng.Intn(len(distractorWords))])
			b.WriteByte(' ')
		}
		b.WriteString(".\n")
	}
	return b.String()
}

// distractorSource emits `count` distractor docs numbered [from, from+count) into
// one bench subcategory, so a whole subcategory's slice ingests in one import.
func distractorSource(subcat string, from, count int) service.DocSource {
	return service.DocSource(func(emit func(path string, content []byte) error) error {
		for i := from; i < from+count; i++ {
			slug := fmt.Sprintf("distractor_%d", i)
			path := models.BuildPath(benchCategory, &subcat, slug)
			if err := emit(path, []byte(distractorContent(subcat, i))); err != nil {
				return err
			}
		}
		return nil
	})
}

// ingestDistractors adds `count` distractor docs (ids [from,from+count)) into each
// subcategory, one ImportDocuments per subcategory across a bounded worker pool.
func ingestDistractors(ctx context.Context, svc *service.MemoryService, tenantID uuid.UUID, subcats []string, from, count, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, subcat := range subcats {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(subcat string) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx := auth.WithTenantID(ctx, tenantID)
			if _, err := svc.ImportDocuments(cctx, tenantID, distractorSource(subcat, from, count)); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("ingest distractors into %s: %w", subcat, err)
				}
				mu.Unlock()
			}
		}(subcat)
	}
	wg.Wait()
	return firstErr
}

// benchCorpusDocs counts the tenant's live bench-category documents (gold +
// distractors) — the "corpus size" each sweep point is measured against.
func benchCorpusDocs(ctx context.Context, db *gorm.DB, tenantID uuid.UUID) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Model(&models.Document{}).
		Where("tenant_id = ? AND category = ? AND archived_at IS NULL", tenantID, benchCategory).
		Count(&n).Error
	return n, err
}

// sliceSubcats returns the unique bench subcategories the slice ingests into
// (one per question), the scopes distractors must grow to crowd retrieval.
func sliceSubcats(slice []Instance) []string {
	seen := make(map[string]struct{}, len(slice))
	out := make([]string, 0, len(slice))
	for _, inst := range slice {
		sc := CanonicalSegment(inst.QuestionID)
		if _, ok := seen[sc]; ok {
			continue
		}
		seen[sc] = struct{}{}
		out = append(out, sc)
	}
	sort.Strings(out)
	return out
}

// scoreSlice scores the whole slice in every retrieval mode, reusing
// evaluateQuestion so a sweep point measures retrieval identical to the base run.
func scoreSlice(ctx context.Context, sections *repository.SectionRepository, db *gorm.DB, embedder service.EmbeddingProvider, tenantID uuid.UUID, slice []Instance, maxK int, mmrLambdas []float64) (map[string]Report, error) {
	var hyb, vec, lex []QuestionResult
	mmr := make(map[string][]QuestionResult, len(mmrLambdas))
	for _, inst := range slice {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		qr, err := evaluateQuestion(ctx, sections, db, embedder, tenantID, inst, maxK, false, mmrLambdas)
		if err != nil {
			slog.Error("scale sweep: evaluate question failed", "question_id", inst.QuestionID, "error", err)
			continue
		}
		hyb = append(hyb, qr.hybrid)
		vec = append(vec, qr.vectorOnly)
		lex = append(lex, qr.lexicalOnly)
		for m, r := range qr.mmr {
			mmr[m] = append(mmr[m], r)
		}
	}
	modes := map[string]Report{
		"hybrid":       Aggregate(hyb),
		"vector_only":  Aggregate(vec),
		"lexical_only": Aggregate(lex),
	}
	for _, l := range mmrLambdas {
		modes[mmrModeKey(l)] = Aggregate(mmr[mmrModeKey(l)])
	}
	return modes, nil
}

// RunScaleSweep grows the bench corpus with irrelevant distractors at ascending
// per-subcategory levels, re-scoring the gold slice at each to quantify how store
// bloat crowds top-K on real embeddings (D8a). Additive: gold docs are untouched.
func RunScaleSweep(ctx context.Context, sections *repository.SectionRepository, db *gorm.DB, embedder service.EmbeddingProvider, svc *service.MemoryService, tenantID uuid.UUID, slice []Instance, sizes []int, maxK int, mmrLambdas []float64, concurrency int, run RunMeta) (ScaleSweepResults, error) {
	subcats := sliceSubcats(slice)
	points := make([]ScalePoint, 0, len(sizes)+1)

	record := func(level int) error {
		modes, err := scoreSlice(ctx, sections, db, embedder, tenantID, slice, maxK, mmrLambdas)
		if err != nil {
			return err
		}
		count, err := benchCorpusDocs(ctx, db, tenantID)
		if err != nil {
			return fmt.Errorf("count corpus at level %d: %w", level, err)
		}
		points = append(points, ScalePoint{DistractorsPerSubcat: level, CorpusDocs: count, Modes: modes})
		slog.Info("scale sweep point recorded", "distractors_per_subcat", level, "corpus_docs", count)
		return nil
	}

	if err := record(0); err != nil { // baseline: no distractors
		return ScaleSweepResults{}, err
	}
	prev := 0
	for _, size := range sizes {
		if ctx.Err() != nil {
			slog.Warn("scale sweep interrupted", "reached_level", prev)
			break
		}
		if add := size - prev; add > 0 {
			if err := ingestDistractors(ctx, svc, tenantID, subcats, prev, add, concurrency); err != nil {
				return ScaleSweepResults{}, err
			}
		}
		prev = size
		if err := record(size); err != nil {
			return ScaleSweepResults{}, err
		}
	}
	return ScaleSweepResults{Run: run, Sizes: sizes, Points: points}, nil
}

// WriteScaleSweepJSON writes r as indented JSON to path.
func WriteScaleSweepJSON(path string, r ScaleSweepResults) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal scale sweep: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// RenderScaleSweepMarkdown renders the sweep as one table per retrieval mode:
// rows are distractor levels (with resulting corpus size), columns the R@k / MRR
// metrics — so recall-vs-corpus-size reads straight down each column.
func RenderScaleSweepMarkdown(r ScaleSweepResults) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# LongMemEval Corpus-Scale Sweep\n\n")
	if len(r.Points) == 0 {
		b.WriteString("_No sweep points recorded._\n")
		return b.String()
	}
	ks := sortedKs(r.Run.KValues)
	writeHeader(&b, r.Run, ks)
	fmt.Fprintf(&b, "Distractors are irrelevant filler docs added per question subcategory; each level re-scores the same gold slice against a larger candidate pool to size how store bloat crowds top-K.\n\n")

	modes := presentModes(r.Points[0].Modes)
	cols := append([]string{"distractors/subcat", "corpus_docs"}, metricColumns(ks)...)
	for _, mode := range modes {
		fmt.Fprintf(&b, "## %s\n\n", mode)
		writeTableHeader(&b, cols)
		for _, p := range r.Points {
			overall := p.Modes[mode].Overall
			row := append([]string{strconv.Itoa(p.DistractorsPerSubcat), strconv.FormatInt(p.CorpusDocs, 10)}, metricValues(overall, ks)...)
			writeTableRow(&b, row)
		}
		b.WriteString("\n")
	}
	return b.String()
}
