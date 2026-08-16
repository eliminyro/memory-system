# usagebench — usage-weighted ranking benchmark

Synthetic, non-circular benchmark that measures whether the opt-in
`MEMORY_USAGE_WEIGHT` ranking term (Phase B) lifts retrieval, finds a good
weight, and maps its judgment-noise tolerance — then emits a pre-registered
go/no-go verdict. Part of the OpenSpec change `phase-b-usage-benchmark`
(design D4/D5/D7).

## What it measures

A topic-structured corpus (`T` topics × docs, relevance = topic membership)
with a deliberately engineered **semantic gap**: some genuinely-relevant docs
are placed *below top-K* on semantic+lexical score, where only accumulated recall
usage can lift them. It then reports held-out **R@5, R@10, MRR, nDCG@10** for a
baseline (`weight=0`, MMR on) against a weight sweep, across a judgment-noise
sweep, plus a **no-gap control** workload that must not be hurt.

### Engineered geometry (why the gap is closable)

Every document embedding is written directly at ingest (via `FakeEmbedder` +
a direct DB update), so cosine to each topic's two query directions is exact.
Each topic has a **primary** direction (where relevant docs live, and where
held-out is measured) and a **secondary** direction (distractor-only):

| kind | cos to primary | cos to secondary | relevant? | role |
|------|---------------|------------------|-----------|------|
| easy | ~0.92 | ~0 | yes | fill baseline top-K |
| distractor | ~0.65 | ~0.65 | **no** | occupy top-K slots; served by both directions |
| gap | ~0.50 (above the 0.40 floor) | ~0 | yes | below top-K until usage lifts it |

With 6 easy + 6 distractors per topic (≥ K=10), gap docs sit below top-K on the
primary query at `weight=0` but within `servedDepth` (20), so they are credited
during history. At `weight>0` the usage term lifts gap docs above the
distractors, raising nDCG@10. The control (gap fraction 0) has no gap docs to
rescue, so usage should not change its already-optimal top-K — bounding the
downside.

## Non-circularity & receipt-level crediting (design D8)

- **History / held-out are disjoint** query slices over the same topics
  (`h-*` vs `o-*` ids; `assertDisjoint` enforces empty intersection).
- **Crediting is receipt-level, exactly as production works.** Per history
  query the harness runs ONE `Search` at `servedDepth`, forms ONE recall-level
  judgment (success iff a truly-relevant doc — topic membership — is in the
  served top-`J`), flips it with independent FP/FN noise, and calls
  `report_recall_outcome` ONCE — which credits every served section together.
  There is **no** per-section / single-section crediting: `report_recall_outcome`
  cannot produce a per-section signal, so faking one would inflate the verdict.
- **The signal is success-rate-under-co-service.** Primary recalls surface a
  relevant doc → succeed → every served section (including co-served
  distractors) gets a hit. Secondary recalls surface only distractors → fail →
  those distractors get misses. So relevant docs accrue a high hit rate and
  distractors a lower one — a deliberately coarse, noisy signal. A mix of
  succeeding and failing recalls (~35% secondary) is required, else all-hits
  gives no discrimination. Counts are **never** injected onto gold docs, and
  held-out gold labels never set a count.

## Architecture (design D7 — in-process)

Unlike `benchmarks/longmemeval/` (external HTTP harness), usagebench imports the
real `internal/repository` + `internal/service` packages and drives them against
a throwaway local Postgres. This lets the weight be swept **per `Search` call**
(`SearchParams.UsageWeight`) with zero server restarts, and exercises the actual
`fuseHybridScored` code path (no client re-implementation to drift).

- Pure files (`corpus.go`, `metrics.go`, `verdict.go`, `output.go`) build and
  unit-test under plain `go test ./benchmarks/usagebench/`.
- The DB-driven runner (`harness.go`, `usagebench_run_test.go`) is gated behind
  `//go:build usagebench` so it is excluded from the normal test path.

## How CI runs it

Needs a local Postgres+pgvector via `TEST_DATABASE_URL` (same as the integration
suite). Never touches prod or the `pe` tenant — every workload gets a fresh
random tenant.

```sh
TEST_DATABASE_URL=postgres://... \
  go test -tags usagebench -run TestUsageBench -timeout 30m ./benchmarks/usagebench/
```

This writes `results.json` (machine-readable matrix + verdict) and `RESULTS.md`
(human tables) into the package directory (override with `USAGEBENCH_OUT`).
The build gates (no DB needed):

```sh
gofmt -l .
go build ./...                 # excludes the tagged runner
go build -tags usagebench ./...
go vet ./... && go vet -tags=integration ./...
golangci-lint run
```

## Knobs (`DefaultRunConfig` / `DefaultParams`)

- **seed** — fixes corpus + query stream; same seed ⇒ identical corpus.
- **weights** — `{0, 0.5, 1, 2, 3, 5}` (weight 0 is the baseline column).
- **noises** — FP=FN `{0, 0.1, 0.2, 0.35, 0.5}`.
- **gap fraction** — gap docs as a fraction of relevant docs (control = 0).
- **secondary fraction** — share of history recalls that are failing (0.35).
- **servedDepth** (20) — history recall depth / receipt size; **judge window `J`** (5).
- **topics / easy / distractors per topic**, **history / held-out query counts**,
  **Zipf skew**, **MMR lambda** (0.9), **held-out limit** (20).

## Honesty caveat

The gap is **engineered to be usage-closable**, so any lift is an **upper bound
on the mechanism**, not a prediction of production benefit — it proves the
mechanism works and maps its noise tolerance, not that the gap is prevalent in
real corpora. The no-gap control bounds the downside. The real-world
gap-prevalence signal is a separate LongMemEval pool-depth probe (design D6,
task 3.6), not part of this harness.
