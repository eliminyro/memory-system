// Command longmemeval runs the LongMemEval retrieval benchmark against
// memory-system's search/import paths. Standalone main package (design D1):
// needs a real Postgres+pgvector and embedder, so it stays out of go test.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/config"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

func main() {
	dataFlag := flag.String("data", "", "path to LongMemEval dataset JSON (required)")
	seedFlag := flag.Int64("seed", 42, "random seed for deterministic slice selection")
	nFlag := flag.String("n", "150", `slice size, or "all" for the full dataset`)
	kFlag := flag.String("k", "5,10", "comma-separated recall@k / MRR@k cutoffs")
	concurrencyFlag := flag.Int("concurrency", 16, "bounded worker pool size for parallel ingest")
	skipIngestFlag := flag.Bool("skip-ingest", false, "skip ingestion; score against the corpus a prior run of the same dataset/seed/n already ingested (fails loudly if that corpus is missing)")
	mmrFlag := flag.String("mmr", "", `comma-separated MMR lambda values in (0,1] (e.g. "0.5,0.7,0.9"); each adds a hybrid_mmr@<lambda> mode reusing the baseline query embedding`)
	outJSONFlag := flag.String("out-json", "benchmarks/longmemeval/results.json", "path to write machine-readable JSON results")
	outMDFlag := flag.String("out-md", "benchmarks/longmemeval/RESULTS.md", "path to write human-readable RESULTS.md")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: longmemeval --data <path> [flags]\n")
		fmt.Fprintf(os.Stderr, "Example: longmemeval --data longmemeval_s_cleaned.json --n 150 --k 5,10\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *dataFlag == "" {
		slog.Error("--data is required")
		flag.Usage()
		os.Exit(1)
	}

	ks, err := parseKs(*kFlag)
	if err != nil {
		slog.Error("invalid --k", "error", err)
		os.Exit(1)
	}
	KValues = ks // override metrics.go's default cutoffs with the requested ones
	maxK := maxInt(ks)

	n, err := parseN(*nFlag)
	if err != nil {
		slog.Error("invalid --n", "error", err)
		os.Exit(1)
	}

	mmrLambdas, err := parseMMRLambdas(*mmrFlag)
	if err != nil {
		slog.Error("invalid --mmr", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}

	if err := database.Migrate(db, cfg.EmbeddingProvider, cfg.EmbeddingModel(), cfg.EmbeddingDimensions, database.TenantColumnDefaults{
		StalenessMode:      cfg.TenantDefaults.StalenessMode,
		DuplicateGuard:     cfg.TenantDefaults.DuplicateGuard,
		CleanupScanEnabled: cfg.TenantDefaults.CleanupScanEnabled,
	}); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	tenantRepo := repository.NewTenantRepository(db)
	benchTenantID, err := EnsureBenchTenant(ctx, tenantRepo)
	if err != nil {
		slog.Error("failed to provision bench tenant", "error", err)
		os.Exit(1)
	}
	slog.Info("bench tenant ready", "tenant_id", benchTenantID, "name", BenchTenantName)

	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)

	embedder, err := service.NewEmbeddingProvider(cfg.EmbeddingProvider, cfg.EmbeddingCfg())
	if err != nil {
		slog.Error("failed to create embedding provider", "error", err)
		os.Exit(1)
	}

	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder, nil, nil, nil, nil, nil, nil, nil, authz.NewPostgresStore(db))

	instances, err := LoadDataset(*dataFlag)
	if err != nil {
		slog.Error("failed to load dataset", "error", err)
		os.Exit(1)
	}

	slice := SelectSlice(instances, *seedFlag, n)
	slog.Info("selected question slice", "n", len(slice), "seed", *seedFlag, "total_available", len(instances))

	if *skipIngestFlag {
		// --skip-ingest (task 4.1) reuses a prior run's corpus: the bench tenant id
		// is fixed and doc paths are deterministic, so the same dataset/seed/n
		// always ingests to the same rows. Verify they're actually there first —
		// scoring an absent/partial corpus would silently read as zero recall.
		if err := VerifyCorpusPresence(ctx, db, benchTenantID, slice); err != nil {
			slog.Error("skip-ingest corpus check failed", "error", err)
			os.Exit(1)
		}
		slog.Info("skip-ingest: reusing existing corpus", "questions", len(slice))
	} else {
		// Idempotent re-runs (task 5.3): the bench tenant id is fixed (EnsureBenchTenant)
		// and every doc path is deterministic (bench/<qid>/<sid>), so StoreDocument's
		// existing get-by-path + overwrite-sections upsert (force=true, already the
		// path ImportDocuments drives) makes a second run replace rather than
		// duplicate documents — no separate pre-run cleanup step is needed.
		if err := ingestSlice(ctx, memorySvc, benchTenantID, slice, *concurrencyFlag); err != nil {
			slog.Error("ingest failed", "error", err)
			os.Exit(1)
		}
		slog.Info("ingest complete", "questions", len(slice))
	}

	// driftSamples caps the mirror-drift check (Risks: "Mirror drift") to the
	// first few successfully-evaluated questions — it's a diagnostic, not part
	// of scoring, and each sample costs two extra candidate-pool-depth queries.
	const driftSampleCap = 3
	driftSamples := 0

	var hybridResults, vectorResults, lexicalResults []QuestionResult
	mmrResults := make(map[string][]QuestionResult, len(mmrLambdas))
	for _, inst := range slice {
		if ctx.Err() != nil {
			slog.Warn("evaluation interrupted before completion", "scored", len(hybridResults), "remaining", len(slice)-len(hybridResults))
			break
		}

		sampleDrift := driftSamples < driftSampleCap
		qr, err := evaluateQuestion(ctx, sectionRepo, db, embedder, benchTenantID, inst, maxK, sampleDrift, mmrLambdas)
		if err != nil {
			slog.Error("failed to evaluate question", "question_id", inst.QuestionID, "error", err)
			continue
		}
		if sampleDrift {
			driftSamples++
		}
		hybridResults = append(hybridResults, qr.hybrid)
		vectorResults = append(vectorResults, qr.vectorOnly)
		lexicalResults = append(lexicalResults, qr.lexicalOnly)
		for mode, res := range qr.mmr {
			mmrResults[mode] = append(mmrResults[mode], res)
		}
	}

	hybridReport := Aggregate(hybridResults)
	vectorReport := Aggregate(vectorResults)
	lexicalReport := Aggregate(lexicalResults)
	printReport("hybrid", hybridReport)
	printReport("vector-only", vectorReport)
	printReport("lexical-only", lexicalReport)

	modes := map[string]Report{
		"hybrid":       hybridReport,
		"vector_only":  vectorReport,
		"lexical_only": lexicalReport,
	}
	// mmrLambdas is already ascending (parseMMRLambdas), so this prints/reports
	// the sweep in the same deterministic order RenderResultsMarkdown uses.
	for _, lambda := range mmrLambdas {
		key := mmrModeKey(lambda)
		report := Aggregate(mmrResults[key])
		modes[key] = report
		printReport(key, report)
	}

	results := BenchmarkResults{
		Run: RunMeta{
			Dataset:          *dataFlag,
			Seed:             *seedFlag,
			N:                n,
			EmbedderProvider: cfg.EmbeddingProvider,
			EmbedderModel:    cfg.EmbeddingModel(),
			EmbedderDims:     cfg.EmbeddingDimensions,
			Commit:           gitCommit(),
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
			KValues:          ks,
		},
		Modes: modes,
	}

	if err := WriteJSON(*outJSONFlag, results); err != nil {
		slog.Error("failed to write JSON results", "path", *outJSONFlag, "error", err)
		os.Exit(1)
	}
	slog.Info("wrote JSON results", "path", *outJSONFlag)

	md := RenderResultsMarkdown(results)
	if err := os.WriteFile(*outMDFlag, []byte(md), 0o644); err != nil {
		slog.Error("failed to write RESULTS.md", "path", *outMDFlag, "error", err)
		os.Exit(1)
	}
	slog.Info("wrote RESULTS.md", "path", *outMDFlag)

	// Also emit both artifacts to stdout: an in-cluster distroless run has no
	// shell/tar to kubectl cp the files out, so the logs are the only channel.
	fmt.Printf("\n===== RESULTS.md BEGIN =====\n%s\n===== RESULTS.md END =====\n", md)
	if data, err := json.MarshalIndent(results, "", "  "); err == nil {
		fmt.Printf("===== results.json BEGIN =====\n%s\n===== results.json END =====\n", data)
	}
}

// ingestSlice drives IngestInstance across the slice with a bounded worker
// pool sized to concurrency (task 5.2): the embedder is single-call/serial
// per section, so parallelism comes from running whole instances/documents
// concurrently rather than from the embed calls themselves. The first
// DocSource-level error is returned once in-flight workers finish draining.
func ingestSlice(ctx context.Context, svc *service.MemoryService, tenantID uuid.UUID, slice []Instance, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, inst := range slice {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(inst Instance) {
			defer wg.Done()
			defer func() { <-sem }()

			result, err := IngestInstance(ctx, svc, tenantID, inst)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if result.Failed > 0 {
				slog.Warn("some documents failed to store", "question_id", inst.QuestionID, "failed", result.Failed, "imported", result.Imported)
			}
		}(inst)
	}
	wg.Wait()
	return firstErr
}

// questionResults bundles one question's per-mode QuestionResult so the
// caller can accumulate all aggregates in one pass over the slice. mmr is
// keyed by mmrModeKey(λ) and only populated for the requested λ sweep.
type questionResults struct {
	hybrid, vectorOnly, lexicalOnly QuestionResult
	mmr                             map[string]QuestionResult
}

// evaluateQuestion embeds inst.Question once and runs the three base
// retrieval modes (task 3) at maxK, plus one additional hybrid+MMR retrieval
// per λ in mmrLambdas (task 5.1) — all reusing the same qEmbedding, so no
// extra embedder calls. sampleDrift additionally runs the mirror-drift check
// (Risks: "Mirror drift") at its own candidate-pool depth (see
// checkMirrorDrift) — a warn-only diagnostic, gated to a sample of questions
// by the caller.
func evaluateQuestion(ctx context.Context, sections *repository.SectionRepository, db *gorm.DB, embedder service.EmbeddingProvider, tenantID uuid.UUID, inst Instance, maxK int, sampleDrift bool, mmrLambdas []float64) (questionResults, error) {
	subcat := CanonicalSegment(inst.QuestionID)

	qEmbedding, err := embedder.Embed(ctx, inst.Question)
	if err != nil {
		return questionResults{}, fmt.Errorf("embed question: %w", err)
	}

	hybrid, err := HybridRetrieve(ctx, sections, tenantID, subcat, qEmbedding, inst.Question, maxK, nil)
	if err != nil {
		return questionResults{}, err
	}
	vectorOnly, err := VectorOnlyRetrieve(ctx, db, tenantID, subcat, qEmbedding, maxK)
	if err != nil {
		return questionResults{}, err
	}
	lexicalOnly, err := LexicalOnlyRetrieve(ctx, db, tenantID, subcat, inst.Question, maxK)
	if err != nil {
		return questionResults{}, err
	}

	if sampleDrift {
		checkMirrorDrift(ctx, db, tenantID, subcat, inst.QuestionID, qEmbedding, inst.Question, hybrid)
	}

	gold := make(map[string]bool, len(inst.AnswerSessionIDs))
	for _, id := range inst.AnswerSessionIDs {
		gold[CanonicalSegment(id)] = true
	}
	effType := EffectiveType(inst)

	mk := func(ranked []RetrievedSection) QuestionResult {
		return QuestionResult{
			QuestionID:    inst.QuestionID,
			EffectiveType: effType,
			Ranked:        RankedSessions(ranked),
			Gold:          gold,
		}
	}

	qr := questionResults{
		hybrid:      mk(hybrid),
		vectorOnly:  mk(vectorOnly),
		lexicalOnly: mk(lexicalOnly),
	}

	if len(mmrLambdas) > 0 {
		qr.mmr = make(map[string]QuestionResult, len(mmrLambdas))
		for _, lambda := range mmrLambdas {
			ranked, err := HybridRetrieve(ctx, sections, tenantID, subcat, qEmbedding, inst.Question, maxK, &lambda)
			if err != nil {
				return questionResults{}, fmt.Errorf("hybrid+mmr retrieve (lambda=%.2f): %w", lambda, err)
			}
			qr.mmr[mmrModeKey(lambda)] = mk(ranked)
		}
	}

	return qr, nil
}

// parseKs parses "--k" (e.g. "5,10") into a positive-int slice.
func parseKs(spec string) ([]int, error) {
	parts := strings.Split(spec, ",")
	ks := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		k, err := strconv.Atoi(p)
		if err != nil || k <= 0 {
			return nil, fmt.Errorf("invalid --k value %q: want a positive integer", p)
		}
		ks = append(ks, k)
	}
	if len(ks) == 0 {
		return nil, fmt.Errorf("--k must list at least one positive integer")
	}
	return ks, nil
}

// parseMMRLambdas parses "--mmr" (e.g. "0.5,0.7,0.9") into an ascending,
// validated λ list; empty/unset input yields nil (no MMR modes measured).
func parseMMRLambdas(spec string) ([]float64, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	lambdas := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lambda, err := strconv.ParseFloat(p, 64)
		if err != nil || lambda <= 0 || lambda > 1 {
			return nil, fmt.Errorf("invalid --mmr value %q: want a number in (0, 1]", p)
		}
		lambdas = append(lambdas, lambda)
	}
	if len(lambdas) == 0 {
		return nil, fmt.Errorf("--mmr must list at least one value in (0, 1]")
	}
	sort.Float64s(lambdas)
	return lambdas, nil
}

func maxInt(ks []int) int {
	m := ks[0]
	for _, k := range ks[1:] {
		if k > m {
			m = k
		}
	}
	return m
}

// parseN parses "--n" ("all" or a positive integer) into SelectSlice's n
// convention (0 = all).
func parseN(spec string) (int, error) {
	if strings.EqualFold(spec, "all") {
		return 0, nil
	}
	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid --n value %q: want a positive integer or \"all\"", spec)
	}
	return n, nil
}

// printReport prints one mode's aggregate + per-question-type breakdown to
// stdout; WriteJSON/RenderResultsMarkdown (output.go) persist the same data.
func printReport(mode string, report Report) {
	fmt.Printf("\n=== %s ===\n", mode)
	printKMetrics("overall", report.Overall)

	types := make([]string, 0, len(report.ByType))
	for t := range report.ByType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		printKMetrics(t, report.ByType[t])
	}
}

func printKMetrics(label string, m KMetrics) {
	fmt.Printf("%-24s n=%-4d", label, m.NumQuestions)
	for _, k := range KValues {
		fmt.Printf(" | R@%d=%.3f fullR@%d=%.3f MRR@%d=%.3f",
			k, m.PartialRecallAtK[k], k, m.FullRecallAtK[k], k, m.MeanMRRAtK[k])
	}
	fmt.Println()
}
