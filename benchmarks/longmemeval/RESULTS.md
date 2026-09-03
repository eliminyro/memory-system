# LongMemEval Retrieval Benchmark Results

> **The default is MMR diversity re-ranking at λ=0.5** (`MEMORY_MMR_LAMBDA`, default `0.5`; set
> `1.0` to disable). The `hybrid` rows are the baseline with MMR off. At **λ=0.5**, MMR lifts
> **full-R@5 88.7% → 91.3%** and **full-R@10 89.3% → 92.0%** over plain hybrid — concentrated on
> multi-evidence questions (knowledge-update 94.7→100, multi-session 86.0→90.7) — and recovers a
> partial-R@5 miss (98.7% → 99.3%). The sweep is monotone around that peak: lower λ over-selects
> (λ≤0.3 dips full-R@5), higher λ reduces to pure relevance (λ→1.0 ≈ plain hybrid). Relevance
> scores are min-max normalized before the MMR combination, so λ acts as a true relevance /
> diversity weight rather than being dominated by the raw fusion scale.
>
> Notes: full-recall is what MMR moves — a single evidence session is easy (partial-R@5 ~99%). The
> **embedder carries the signal** here (hybrid ≡ vector-only to the digit), so fusion is for
> score-correctness and robustness, not extra recall. Runs always re-ingest so corpus and query
> embeddings share one embedding epoch; `--skip-ingest` is valid only for relative within-run λ
> deltas (fresh query vectors against a stale corpus drift and understate every mode).

- Dataset: `https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json`
- Seed: 42
- N: 150
- Embedder: gcp / text-embedding-005 (768 dims)
- Version: `v1.0.0`
- Timestamp: 2026-08-28T18:23:25Z
- K values: 5, 10

Metrics are session-level: a retrieved section's session is its owning document's slug; the ranked session list is retrieved sections deduped to first occurrence.

## Overall

| Mode | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| hybrid | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| vector_only | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| lexical_only | 150 | 8.7% | 3.3% | 8.7% | 3.3% | 0.087 | 0.087 |
| hybrid_mmr@0.30 | 150 | 99.3% | 88.0% | 99.3% | 91.3% | 0.951 | 0.951 |
| hybrid_mmr@0.40 | 150 | 99.3% | 90.7% | 99.3% | 92.0% | 0.954 | 0.954 |
| hybrid_mmr@0.50 | 150 | 99.3% | 91.3% | 99.3% | 92.0% | 0.956 | 0.956 |
| hybrid_mmr@0.60 | 150 | 98.7% | 90.7% | 99.3% | 91.3% | 0.954 | 0.956 |
| hybrid_mmr@0.70 | 150 | 98.7% | 90.0% | 99.3% | 90.7% | 0.954 | 0.956 |
| hybrid_mmr@0.90 | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |

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

### hybrid_mmr@0.30

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 89.5% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 83.7% | 100.0% | 88.4% | 0.961 | 0.961 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.857 | 0.857 |

### hybrid_mmr@0.40

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 94.7% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 90.7% | 100.0% | 90.7% | 0.961 | 0.961 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.867 | 0.867 |

### hybrid_mmr@0.50

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 90.7% | 100.0% | 90.7% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 97.5% | 80.0% | 97.5% | 82.5% | 0.872 | 0.872 |

### hybrid_mmr@0.60

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 88.4% | 100.0% | 88.4% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 95.0% | 80.0% | 97.5% | 82.5% | 0.867 | 0.871 |

### hybrid_mmr@0.70

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 86.0% | 100.0% | 86.0% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 95.0% | 80.0% | 97.5% | 82.5% | 0.867 | 0.871 |

### hybrid_mmr@0.90

| Type | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| abstention | 9 | 100.0% | 88.9% | 100.0% | 88.9% | 1.000 | 1.000 |
| knowledge-update | 19 | 100.0% | 94.7% | 100.0% | 94.7% | 1.000 | 1.000 |
| multi-session | 43 | 100.0% | 86.0% | 100.0% | 86.0% | 0.965 | 0.965 |
| single-session-assistant | 17 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-preference | 7 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| single-session-user | 15 | 100.0% | 100.0% | 100.0% | 100.0% | 1.000 | 1.000 |
| temporal-reasoning | 40 | 95.0% | 77.5% | 97.5% | 80.0% | 0.867 | 0.871 |


