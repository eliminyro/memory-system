# LongMemEval Retrieval Benchmark Results

> **Shipped default is MMR diversity re-ranking at λ=0.9** (`MEMORY_MMR_LAMBDA`, default `0.9`;
> set `1.0` to disable). The `hybrid` rows below are the pre-MMR baseline (MMR off); the
> `hybrid_mmr@0.90` rows are the shipped default. MMR lifts **full-R@5 88.7% → 90.7%** and
> **full-R@10 89.3% → 92.0%**, concentrated on multi-evidence questions
> (knowledge-update 94.7→100, multi-session full-R@10 86→90.7, temporal-reasoning 77.5→80.0),
> with no regressions on single-evidence types. λ was swept {0.5,0.7,0.85,0.9,0.95}: aggressive
> diversity (λ≤0.7) *hurts* full-R@5 (λ=0.5 → 82.0), so 0.9 is the calibrated operating point.
> Reproduced across two independent fresh-ingest runs.
>
> Note: results here always re-ingest the corpus (corpus + query embeddings share one embedding
> epoch); scoring a stale corpus with fresh query vectors (`--skip-ingest` against an old run)
> drifts and understates every mode — use `--skip-ingest` only for relative within-run deltas.

- Dataset: `https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json`
- Seed: 42
- N: 150
- Embedder: gcp / text-embedding-005 (768 dims)
- Commit: `e70fcaf`
- Timestamp: 2026-08-15T07:29:48Z
- K values: 5, 10

Metrics are session-level: a retrieved section's session is its owning document's slug; the ranked session list is retrieved sections deduped to first occurrence.

## Overall

| Mode | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| hybrid | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| vector_only | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| lexical_only | 150 | 8.7% | 3.3% | 8.7% | 3.3% | 0.087 | 0.087 |
| hybrid_mmr@0.85 | 150 | 99.3% | 90.0% | 99.3% | 93.3% | 0.952 | 0.952 |
| hybrid_mmr@0.90 | 150 | 99.3% | 90.7% | 99.3% | 92.0% | 0.955 | 0.955 |
| hybrid_mmr@0.95 | 150 | 99.3% | 90.0% | 99.3% | 90.7% | 0.956 | 0.956 |

## Per-Question-Type

### hybrid

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 94.7% | 100.0% | 94.7% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 86.0% | 100.0% | 86.0% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 95.0% | 77.5% | 97.5% | 80.0% | 0.867 | 0.871 |

### vector_only

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 94.7% | 100.0% | 94.7% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 86.0% | 100.0% | 86.0% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 95.0% | 77.5% | 97.5% | 80.0% | 0.867 | 0.871 |

### lexical_only

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 0.0% | 0.0% | 0.0% | 0.0% | 0.000 | 0.000 |
| knowledge-update | 19 | 26.3% | 0.0% | 26.3% | 0.0% | 0.263 | 0.263 |
| multi-session | 43 | 4.7% | 0.0% | 4.7% | 0.0% | 0.047 | 0.047 |
| single-session-assistant | 17 | 0.0% | 0.0% | 0.0% | 0.0% | 0.000 | 0.000 |
| single-session-preference | 7 | 0.0% | 0.0% | 0.0% | 0.0% | 0.000 | 0.000 |
| single-session-user | 15 | 33.3% | 33.3% | 33.3% | 33.3% | 0.333 | 0.333 |
| temporal-reasoning | 40 | 2.5% | 0.0% | 2.5% | 0.0% | 0.025 | 0.025 |

### hybrid_mmr@0.85

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 86.0% | 100.0% | 95.3% | 0.961 | 0.961 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.863 | 0.863 |

### hybrid_mmr@0.90

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 88.4% | 100.0% | 90.7% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.870 | 0.870 |

### hybrid_mmr@0.95

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 94.7% | 100.0% | 94.7% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 88.4% | 100.0% | 88.4% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.872 | 0.872 |
