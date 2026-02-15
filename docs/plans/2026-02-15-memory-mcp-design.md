# Memory MCP Server — Design

## Overview

Replace the file-based Claude Code memory system (`~/.claude/context/memory/`) with a Postgres-backed MCP server. Semantic search via pgvector + local embeddings (Ollama). Deployed on the NAS via Docker Compose.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Delivery | Go MCP server (HTTP) | Matches Hilo patterns, `modelcontextprotocol/go-sdk` |
| Database | Postgres 17 + pgvector (dedicated sidecar) | Self-contained, no shared infra dependency |
| Embeddings | Local model via Ollama on NAS | 96GB RAM NAS, no external API dependency |
| Migration | One-time import CLI | Parse existing markdown files, bulk-insert |
| Granularity | Per-section (split by `##` headings) | Fine-grained retrieval, sections linked by document |
| Scoping | Hierarchical (category/subcategory/slug) | Mirrors current file structure |

## Data Model

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  category      TEXT NOT NULL,      -- "learnings", "preferences", "projects"
  subcategory   TEXT,               -- "go", "infrastructure", "hilo" (nullable)
  slug          TEXT NOT NULL,      -- "gorm", "terraform", "state"
  title         TEXT NOT NULL,      -- extracted from first # heading
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(category, subcategory, slug)
);

CREATE TABLE sections (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id   UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  ordinal       INT NOT NULL,
  heading       TEXT,               -- the ## heading text (nullable for preamble)
  content       TEXT NOT NULL,
  embedding     VECTOR(768),        -- dimension depends on model
  tsv           TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sections_document_ordinal ON sections(document_id, ordinal);
CREATE INDEX idx_sections_embedding ON sections USING ivfflat (embedding vector_cosine_ops) WITH (lists = 10);
CREATE INDEX idx_sections_tsv ON sections USING gin(tsv);
```

## MCP Tools

| Tool | Description | Key params |
|------|-------------|------------|
| `search_memory` | Hybrid semantic + keyword search. Returns ranked sections with document context. | `query`, `category?`, `subcategory?`, `limit?` |
| `get_document` | Fetch all sections of a document by path. | `category`, `subcategory?`, `slug` |
| `store_memory` | Create or update a document. Parses markdown into sections, generates embeddings. | `category`, `subcategory?`, `slug`, `content` (markdown) |
| `update_section` | Update a single section's content. Re-embeds automatically. | `section_id`, `content` |
| `delete_document` | Remove a document and all its sections. | `document_id` or `category/subcategory/slug` |
| `list_documents` | Browse the hierarchy. | `category?`, `subcategory?` |

### Search Algorithm

```sql
WITH semantic AS (
  SELECT s.id, s.document_id, s.heading, s.content,
         1 - (s.embedding <=> $1) AS score
  FROM sections s
  JOIN documents d ON d.id = s.document_id
  WHERE ($2 IS NULL OR d.category = $2)
    AND ($3 IS NULL OR d.subcategory = $3)
  ORDER BY s.embedding <=> $1
  LIMIT 20
),
keyword AS (
  SELECT s.id, ts_rank(s.tsv, plainto_tsquery($4)) AS score
  FROM sections s
  WHERE s.tsv @@ plainto_tsquery($4)
)
SELECT sem.id, sem.document_id, sem.heading, sem.content,
       (0.7 * sem.score + 0.3 * COALESCE(kw.score, 0)) AS combined_score
FROM semantic sem
LEFT JOIN keyword kw ON kw.id = sem.id
ORDER BY combined_score DESC
LIMIT $5;
```

## Architecture

```
NAS (Docker Compose)
├── memory-mcp  (Go, :8080)  ──▶  postgres (pgvector, :5432)
│       │
│       └── HTTP ──▶  ollama (:11434)
│
└── Reverse proxy ──▶ https://memory-mcp.eliminyro.me/mcp
                            ▲
                      Claude Code (MCP client)
```

## Project Structure

```
memory-mcp/
├── cmd/
│   ├── server/main.go        # Entrypoint, config, wire dependencies
│   └── import/main.go        # One-time migration CLI
├── internal/
│   ├── config/config.go      # Env-based config (caarlos0/env)
│   ├── models/models.go      # GORM models (Document, Section)
│   ├── repository/
│   │   ├── document.go       # Document CRUD
│   │   └── section.go        # Section CRUD + search queries
│   ├── service/
│   │   ├── memory.go         # Business logic, markdown parsing
│   │   └── embedder.go       # Ollama client for embeddings
│   └── mcp/
│       ├── server.go         # MCP server setup (SDK)
│       └── tools.go          # Tool handlers
├── docker-compose.yml        # NAS deployment (3 services)
├── Dockerfile                # Multi-stage Go build
└── go.mod
```

## Docker Compose (NAS)

```yaml
services:
  memory-mcp:
    build: .
    ports:
      - "8090:8080"
    environment:
      DATABASE_URL: postgres://memory:memory@postgres:5432/memory?sslmode=disable
      OLLAMA_URL: http://ollama:11434
      OLLAMA_MODEL: nomic-embed-text
    depends_on:
      postgres:
        condition: service_healthy
      ollama:
        condition: service_started

  postgres:
    image: pgvector/pgvector:pg17
    volumes:
      - pgdata:/var/lib/postgresql/data
    environment:
      POSTGRES_DB: memory
      POSTGRES_USER: memory
      POSTGRES_PASSWORD: memory
    healthcheck:
      test: ["CMD-LINE", "pg_isready -U memory"]
      interval: 5s
      retries: 5

  ollama:
    image: ollama/ollama
    volumes:
      - ollama_models:/root/.ollama

volumes:
  pgdata:
  ollama_models:
```

## Claude Code Integration

### MCP Config (`~/.claude.json`)

```json
"memory-mcp": {
  "type": "http",
  "url": "https://memory-mcp.eliminyro.me/mcp"
}
```

### What Moves to Postgres

Everything under `~/.claude/context/memory/`:
- `learnings/**/*.md` (43 files, ~2800 lines)
- `preferences/*.md`
- `projects/*/state.md`
- `work-status.md`
- `manifest.json` (eliminated — database is the manifest)

### What Stays File-Based

- `CLAUDE.md` files (Claude Code auto-loads these natively)
- Project-specific `CLAUDE.md` instructions
- Hooks that inject CLAUDE.md content

### CLAUDE.md Updates

Replace file-based memory instructions with MCP tool usage:
- `search_memory` replaces grep/read pattern
- `store_memory` replaces Write to markdown files
- `get_document` replaces direct file reads
- `list_documents` replaces manifest.json routing

### Hook Changes

Simplify `load-context.sh`:
- Keep: CLAUDE.md injection, project detection
- Remove: memory file reading, manifest routing
- Optionally: call MCP HTTP API to inject key context at session start
