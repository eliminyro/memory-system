# LongMemEval Retrieval Benchmark Results

- Dataset: `https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json`
- Seed: 42
- N: 150
- Embedder: gcp / text-embedding-005 (768 dims)
- Commit: `64f7ee8`
- Timestamp: 2026-08-14T05:15:55Z
- K values: 5, 10

Metrics are session-level: a retrieved section's session is its owning document's slug; the ranked session list is retrieved sections deduped to first occurrence.

## Overall

| Mode | N | partial-R@5 | full-R@5 | partial-R@10 | full-R@10 | MRR@5 | MRR@10 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| hybrid | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| vector_only | 150 | 98.7% | 88.7% | 99.3% | 89.3% | 0.954 | 0.956 |
| lexical_only | 150 | 8.7% | 3.3% | 8.7% | 3.3% | 0.087 | 0.087 |

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

## Run notes

Produced on a throwaway prod-parity instance on the a11s/jx GKE cluster (`memory-mcp-bench`,
Vertex `text-embedding-005` via the `memory-mcp` workload-identity SA), isolated from prod —
**not** production. Ran as a Kubernetes Job (`--concurrency 8`); ~14 min wall, 0 rate-limit
errors, 0 failed imports (the batched GCP embedder keeps bulk ingest under the Vertex quota).
The harness's in-pod git-sha capture returns `unknown` (no `.git` in the distroless image); the
`Commit` above is the producing branch commit, stamped by hand.

**Reading the baseline:** `hybrid` and `vector_only` are identical to 4 decimals — the lexical
arm (`plainto_tsquery`) recalls almost nothing on LongMemEval's paraphrased questions
(partial-R@5 8.7%), so the shipped weighted-sum fusion is effectively vector-only here despite
`fuseLexWeight=0.6`. `temporal-reasoning` is the hardest slice. This is the reference point for
the upcoming RRF / MMR / re-weighting changes.
