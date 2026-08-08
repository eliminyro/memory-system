# Embedding providers

memory-system embeds documents with exactly **one** provider, chosen at startup
by `EMBEDDING_PROVIDER`. The embedding is stored in a fixed-dimension `pgvector`
column, so the provider, model, and dimension are effectively **frozen for the
life of a corpus** — see [Changing your embedding model](#changing-your-embedding-model).

## Provider matrix

| `EMBEDDING_PROVIDER` | Env vars | Example model | Typical dimension | Credentials |
|----------------------|----------|---------------|-------------------|-------------|
| `ollama`  | `OLLAMA_URL`, `OLLAMA_MODEL` | `nomic-embed-text` | 768 | none |
| `gcp`     | `GCP_PROJECT`, `GCP_LOCATION`, `GCP_EMBEDDING_MODEL` | `text-embedding-005` | 768 | Application Default Credentials (ADC) |
| `openai`  | `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | 1536 (configurable) | `OPENAI_API_KEY` (optional for local servers) |
| `aws`     | `AWS_REGION`, `AWS_EMBEDDING_MODEL` | `amazon.titan-embed-text-v2:0` | 1024 | standard AWS chain (env / shared config / IAM role) |
| `fake`    | — | — | `EMBEDDING_DIMENSIONS` | none (deterministic; tests only) |

`EMBEDDING_DIMENSIONS` (default `768`) **must** equal the dimension your model
emits. The startup guard refuses to run a populated corpus whose stored
dimension differs (see below).

### `openai` — any OpenAI-compatible endpoint

`EMBEDDING_PROVIDER=openai` speaks the standard `POST {base}/embeddings`
protocol, so one provider covers **OpenAI, Azure OpenAI, vLLM, LM Studio,
LocalAI, and HuggingFace TEI**. Point `OPENAI_BASE_URL` at the API root that
exposes `/embeddings`:

| Target | `OPENAI_BASE_URL` |
|--------|-------------------|
| OpenAI | `https://api.openai.com/v1` (default) |
| Azure OpenAI | `https://<resource>.openai.azure.com/openai/deployments/<deployment>` (append `?api-version=...`) |
| vLLM / LM Studio / LocalAI / TEI | e.g. `http://localhost:1234/v1` |

- `OPENAI_API_KEY` is optional; when set it is sent as `Authorization: Bearer <key>`. Most local servers ignore it.
- For models that support **output-dimension truncation** (`text-embedding-3-*`),
  `EMBEDDING_DIMENSIONS` is sent as the requested output size, so you can pin the
  corpus to e.g. 768. Models without truncation support (legacy `ada-002`)
  require `EMBEDDING_DIMENSIONS` set to their native size.

### `aws` — Bedrock

`EMBEDDING_PROVIDER=aws` invokes AWS Bedrock. Credentials are resolved from the
**standard AWS chain** (environment variables, shared config/profile, or an
attached IAM role) — never from application config. Supported model families:

- **Titan Text Embeddings** — `amazon.titan-embed-text-v2:0` (honors `EMBEDDING_DIMENSIONS`), `amazon.titan-embed-text-v1`.
- **Cohere Embed** — `cohere.embed-english-v3`, `cohere.embed-multilingual-v3`.

The family is dispatched by the `AWS_EMBEDDING_MODEL` prefix (`amazon.titan…` vs
`cohere.…`).

## Dimension consistency guard

At startup, `Migrate` reconciles the configured dimension with the corpus:

- **Empty corpus / fresh DB** → the provider's dimension is adopted and recorded
  (in `embedding_metadata` + the `sections.embedding` column type).
- **Populated corpus, matching dimension** → starts normally.
- **Populated corpus, different dimension** → **fatal**: the server refuses to
  boot with an error naming both dimensions, rather than writing short/corrupt
  vectors.

A companion identity guard additionally refuses a **provider or model** change on
a populated corpus (even at the same dimension), because cosine similarity, the
duplicate guard, and retention scoring are only comparable within one embedding
space.

## Changing your embedding model

Because vectors are model-specific, switching provider/model/dimension on a
populated corpus requires **re-embedding every document** — there is no online
migration. The corpus text lives in `sections.content`, but the durable source
of truth is your document export.

1. **Back up.** Export your documents (the markdown source), and snapshot the DB.
2. **Provision a clean corpus.** Either point at a fresh empty database, or clear
   the existing one (`TRUNCATE sections, documents, ... CASCADE` and
   `DELETE FROM embedding_metadata`).
3. **Configure the new model.** Set `EMBEDDING_PROVIDER`, the provider's model
   env var, and `EMBEDDING_DIMENSIONS` to the new model's output dimension.
4. **Re-embed.** Re-import your source documents (e.g. via `cmd/import`); each
   section is embedded with the new model into the fresh corpus.
5. **Cut over.** Point traffic at the re-embedded instance.

Attempting to start against the *old* populated corpus with the new model/dimension
will fail fast by design — that error is the guard doing its job, not a bug.
