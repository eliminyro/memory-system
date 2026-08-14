# LongMemEval retrieval benchmark

A reproducible harness that measures memory-system's **retrieval** quality on
[LongMemEval](https://github.com/xiaowu0162/LongMemEval) (Wu et al., ICLR 2025) — a
needle-in-a-haystack long-term-memory benchmark. It ingests each question's chat-history
haystack through the normal import path, exercises the existing search pipeline in three
modes, and reports session-level recall@k / MRR.

It is **developer tooling**, not part of the server: `cmd/server` links none of it, and it is
run on demand — it is **not** wired into per-push CI.

## What it measures

For each question, every haystack **session** becomes one document (`bench/<question_id>/<session_id>`)
and every **turn** a `## turn` section. The question is then searched, scoped to its own
haystack. Metrics are **session-level**: a retrieved section's session is its owning document's
slug; the ranked session list is retrieved sections deduped to first occurrence.

- **partial-R@k** — fraction of questions with ≥1 gold session (`answer_session_ids`) in the top-k.
- **full-R@k** — fraction with *all* gold sessions in the top-k.
- **MRR** — mean reciprocal rank of the first gold session.

Reported per mode and per `question_type` (`_abs` → `abstention`), at k ∈ {5, 10}.

Three retrieval **modes** run over the same corpus so the shipped default can be compared to
each single arm:
- **hybrid** — the shipped `SectionRepository.HybridSearch`, unchanged.
- **vector-only** / **lexical-only** — read queries that mirror `section.go`'s `semantic` /
  `keyword` CTEs (pinned to the mirrored commit; a sampled drift check guards against them
  diverging from the real fusion pool).

## Prerequisites

1. **Dataset** (not vendored — fetch at run time):
   ```sh
   mkdir -p data
   wget -O data/longmemeval_s_cleaned.json \
     https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json
   ```
2. **A pgvector Postgres**, reachable at `DATABASE_URL` (config default
   `postgres://memory:memory@localhost:5432/memory`). The harness runs `database.Migrate` on
   startup, so any empty pgvector Postgres works — point `DATABASE_URL` at your own instance.
   Our committed baseline port-forwards the throwaway in-cluster bench instance (see below).
3. **An embedder.** Configured exactly like the server, via env:
   - Local, creds-free (default): `ollama` — `ollama pull nomic-embed-text` and have Ollama
     running (`EMBEDDING_PROVIDER=ollama`, `OLLAMA_MODEL=nomic-embed-text`, `EMBEDDING_DIMENSIONS=768`).
   - Prod-parity (the committed baseline): `EMBEDDING_PROVIDER=gcp`,
     `GCP_EMBEDDING_MODEL=text-embedding-005`, `EMBEDDING_DIMENSIONS=768`, `GCP_PROJECT=…`,
     `GCP_LOCATION=…` (needs Vertex credentials / a service account).

## Run

```sh
go run ./benchmarks/longmemeval \
  --data data/longmemeval_s_cleaned.json \
  --seed 42 --n 150 --k 5,10 --concurrency 16
```

Flags: `--data` (required), `--seed` (default 42), `--n` (slice size or `all`, default 150),
`--k` (CSV, default `5,10`), `--concurrency` (ingest workers, default 16), `--out-json`
(default `benchmarks/longmemeval/results.json`), `--out-md` (default
`benchmarks/longmemeval/RESULTS.md`).

The slice is a **seeded, deterministic** subset — same `--seed`+`--n` selects the same
questions. Ingestion is idempotent (fixed bench tenant + upsert on deterministic paths), so
re-runs overwrite rather than duplicate. Ingestion is parallelized across questions because the
embedder is single-call per section.

Outputs: `results.json` (machine-readable, with run provenance — dataset/seed/n/embedder/commit)
and `RESULTS.md` (the human-readable tables).

### Committed baseline

The committed `RESULTS.md` is produced against a throwaway prod-parity instance on our cluster
(Vertex `text-embedding-005`, 768-dim), **not** production, so ~40k benchmark docs never touch
the live DB:

```sh
kubectl -n a11s port-forward svc/memory-mcp-bench-postgres 5432:5432   # the throwaway bench instance
EMBEDDING_PROVIDER=gcp GCP_EMBEDDING_MODEL=text-embedding-005 EMBEDDING_DIMENSIONS=768 \
  GCP_PROJECT=<project> GCP_LOCATION=<region> \
  go run ./benchmarks/longmemeval --data data/longmemeval_s_cleaned.json --seed 42 --n 150
```

## Scope

This harness changes **no** shipped ranking behavior — it only reads through existing code
paths. It exists so that upcoming ranking changes (RRF fusion, MMR diversity, `doc_type`
filtering, duplicate-guard rework) can be measured as before/after rather than by feel.
