# Memory MCP Server — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Postgres-backed MCP server that replaces file-based Claude Code memory with semantic + keyword search.

**Architecture:** Go MCP server using `modelcontextprotocol/go-sdk`, GORM for Postgres+pgvector, Ollama for local embeddings. Deployed via Docker Compose on NAS.

**Tech Stack:** Go 1.24, GORM, pgvector, Ollama, MCP SDK v1.2.0, caarlos0/env

---

### Task 1: Project Scaffolding

**Files:**
- Create: `go.mod`
- Create: `cmd/server/main.go`
- Create: `internal/config/config.go`
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `.gitignore`

**Step 1: Initialize Go module**

```bash
cd ~/mystuff/goprojects/memory-mcp
go mod init github.com/eliminyro/memory-mcp
```

**Step 2: Create `.gitignore`**

```
/memory-mcp
/import
*.exe
.env
```

**Step 3: Create config**

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"

	"github.com/caarlos0/env/v10"
)

type Config struct {
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://memory:memory@localhost:5432/memory?sslmode=disable"`
	OllamaURL   string `env:"OLLAMA_URL" envDefault:"http://localhost:11434"`
	OllamaModel string `env:"OLLAMA_MODEL" envDefault:"nomic-embed-text"`
	ServerAddr  string `env:"SERVER_ADDR" envDefault:":8080"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}
```

**Step 4: Create minimal main.go**

Create `cmd/server/main.go`:

```go
package main

import (
	"log/slog"
	"os"

	"github.com/eliminyro/memory-mcp/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("memory-mcp starting", "addr", cfg.ServerAddr)
}
```

**Step 5: Create Dockerfile**

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /memory-mcp ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /memory-mcp /memory-mcp
ENTRYPOINT ["/memory-mcp"]
```

**Step 6: Create docker-compose.yml**

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
    restart: unless-stopped

  postgres:
    image: pgvector/pgvector:pg17
    volumes:
      - pgdata:/var/lib/postgresql/data
    environment:
      POSTGRES_DB: memory
      POSTGRES_USER: memory
      POSTGRES_PASSWORD: memory
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "memory"]
      interval: 5s
      retries: 5
    restart: unless-stopped

  ollama:
    image: ollama/ollama
    volumes:
      - ollama_models:/root/.ollama
    restart: unless-stopped

volumes:
  pgdata:
  ollama_models:
```

**Step 7: Install dependencies and verify build**

```bash
go get github.com/caarlos0/env/v10
go get gorm.io/gorm
go get gorm.io/driver/postgres
go get github.com/google/uuid
go get github.com/modelcontextprotocol/go-sdk/mcp
go get github.com/pgvector/pgvector-go
go build ./cmd/server
```

**Step 8: Commit**

```bash
git add -A
git commit -m "Scaffold project with config, Dockerfile, docker-compose"
```

---

### Task 2: GORM Models

**Files:**
- Create: `internal/models/document.go`
- Create: `internal/models/section.go`

**Step 1: Create Document model**

Create `internal/models/document.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Category    string    `gorm:"size:50;not null;uniqueIndex:idx_doc_path" json:"category"`
	Subcategory *string   `gorm:"size:100;uniqueIndex:idx_doc_path" json:"subcategory,omitempty"`
	Slug        string    `gorm:"size:100;not null;uniqueIndex:idx_doc_path" json:"slug"`
	Title       string    `gorm:"size:500;not null" json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	Sections []Section `gorm:"foreignKey:DocumentID;constraint:OnDelete:CASCADE" json:"sections,omitempty"`
}

func (Document) TableName() string {
	return "documents"
}

// Path returns the hierarchical path like "learnings/go/gorm"
func (d Document) Path() string {
	if d.Subcategory != nil {
		return d.Category + "/" + *d.Subcategory + "/" + d.Slug
	}
	return d.Category + "/" + d.Slug
}
```

**Step 2: Create Section model**

Create `internal/models/section.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

type Section struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	DocumentID uuid.UUID      `gorm:"type:uuid;not null;index:idx_section_doc_ord" json:"document_id"`
	Ordinal    int            `gorm:"not null;index:idx_section_doc_ord" json:"ordinal"`
	Heading    *string        `gorm:"size:500" json:"heading,omitempty"`
	Content    string         `gorm:"type:text;not null" json:"content"`
	Embedding  pgvector.Vector `gorm:"type:vector(768)" json:"-"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`

	Document *Document `gorm:"foreignKey:DocumentID" json:"document,omitempty"`
}

func (Section) TableName() string {
	return "sections"
}
```

**Step 3: Verify build**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add internal/models/
git commit -m "Add Document and Section GORM models"
```

---

### Task 3: Database Connection + Migration

**Files:**
- Create: `internal/database/database.go`
- Modify: `cmd/server/main.go`

**Step 1: Create database package**

Create `internal/database/database.go`:

```go
package database

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-mcp/internal/models"
)

func Connect(databaseURL string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	// Enable pgvector extension
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return nil, fmt.Errorf("enable pgvector: %w", err)
	}

	return db, nil
}

func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&models.Document{}, &models.Section{}); err != nil {
		return fmt.Errorf("auto-migrate: %w", err)
	}

	// Create tsvector generated column and GIN index (AutoMigrate can't do these)
	migrations := []string{
		`DO $$ BEGIN
			ALTER TABLE sections ADD COLUMN IF NOT EXISTS tsv tsvector
				GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_sections_tsv ON sections USING gin(tsv)`,
		`CREATE INDEX IF NOT EXISTS idx_sections_embedding ON sections USING ivfflat (embedding vector_cosine_ops) WITH (lists = 10)`,
	}

	for _, m := range migrations {
		if err := db.Exec(m).Error; err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}

	return nil
}
```

**Step 2: Wire database into main.go**

Update `cmd/server/main.go`:

```go
package main

import (
	"log/slog"
	"os"

	"github.com/eliminyro/memory-mcp/internal/config"
	"github.com/eliminyro/memory-mcp/internal/database"
)

func main() {
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

	if err := database.Migrate(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	slog.Info("memory-mcp starting", "addr", cfg.ServerAddr)
	_ = db // will be used in next task
}
```

**Step 3: Verify build**

```bash
go build ./cmd/server
```

**Step 4: Verify against real Postgres** (optional, requires docker-compose up for postgres)

```bash
docker compose up -d postgres
DATABASE_URL="postgres://memory:memory@localhost:5432/memory?sslmode=disable" go run ./cmd/server
```

Expected: "memory-mcp starting" log line, tables created in Postgres.

**Step 5: Commit**

```bash
git add internal/database/ cmd/server/main.go
git commit -m "Add database connection and auto-migration with pgvector"
```

---

### Task 4: Ollama Embedder Service

**Files:**
- Create: `internal/service/embedder.go`

**Step 1: Create embedder**

Create `internal/service/embedder.go`:

```go
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pgvector/pgvector-go"
)

type Embedder struct {
	url    string
	model  string
	client *http.Client
}

func NewEmbedder(ollamaURL, model string) *Embedder {
	return &Embedder{
		url:   ollamaURL,
		model: model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed generates an embedding vector for the given text.
func (e *Embedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return pgvector.Vector{}, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return pgvector.Vector{}, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Embeddings) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no embeddings returned")
	}

	return pgvector.NewVector(result.Embeddings[0]), nil
}

// EmbedBatch generates embeddings for multiple texts.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([]pgvector.Vector, error) {
	vectors := make([]pgvector.Vector, len(texts))
	for i, text := range texts {
		v, err := e.Embed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("embed text %d: %w", i, err)
		}
		vectors[i] = v
	}
	return vectors, nil
}
```

**Step 2: Verify build**

```bash
go build ./...
```

**Step 3: Commit**

```bash
git add internal/service/embedder.go
git commit -m "Add Ollama embedder client"
```

---

### Task 5: Repository Layer

**Files:**
- Create: `internal/repository/document.go`
- Create: `internal/repository/section.go`

**Step 1: Create document repository**

Create `internal/repository/document.go`:

```go
package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-mcp/internal/models"
)

type DocumentRepository struct {
	db *gorm.DB
}

func NewDocumentRepository(db *gorm.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

func (r *DocumentRepository) Create(ctx context.Context, doc *models.Document) error {
	return r.db.WithContext(ctx).Create(doc).Error
}

func (r *DocumentRepository) GetByPath(ctx context.Context, category string, subcategory *string, slug string) (*models.Document, error) {
	var doc models.Document
	q := r.db.WithContext(ctx).Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	if err := q.Preload("Sections", func(db *gorm.DB) *gorm.DB {
		return db.Order("ordinal ASC")
	}).First(&doc).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Document, error) {
	var doc models.Document
	if err := r.db.WithContext(ctx).
		Preload("Sections", func(db *gorm.DB) *gorm.DB {
			return db.Order("ordinal ASC")
		}).
		First(&doc, id).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) List(ctx context.Context, category *string, subcategory *string) ([]models.Document, error) {
	var docs []models.Document
	q := r.db.WithContext(ctx)
	if category != nil {
		q = q.Where("category = ?", *category)
	}
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	}
	if err := q.Order("category, subcategory, slug").Find(&docs).Error; err != nil {
		return nil, err
	}
	return docs, nil
}

func (r *DocumentRepository) Upsert(ctx context.Context, doc *models.Document) error {
	existing, err := r.GetByPath(ctx, doc.Category, doc.Subcategory, doc.Slug)
	if err == nil {
		doc.ID = existing.ID
		doc.CreatedAt = existing.CreatedAt
		return r.db.WithContext(ctx).Save(doc).Error
	}
	return r.db.WithContext(ctx).Create(doc).Error
}

func (r *DocumentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Delete(&models.Document{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("document not found")
	}
	return nil
}

func (r *DocumentRepository) DeleteByPath(ctx context.Context, category string, subcategory *string, slug string) error {
	q := r.db.WithContext(ctx).Where("category = ? AND slug = ?", category, slug)
	if subcategory != nil {
		q = q.Where("subcategory = ?", *subcategory)
	} else {
		q = q.Where("subcategory IS NULL")
	}
	result := q.Delete(&models.Document{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("document not found")
	}
	return nil
}
```

**Step 2: Create section repository**

Create `internal/repository/section.go`:

```go
package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-mcp/internal/models"
)

type SectionRepository struct {
	db *gorm.DB
}

func NewSectionRepository(db *gorm.DB) *SectionRepository {
	return &SectionRepository{db: db}
}

type SearchResult struct {
	SectionID  uuid.UUID `json:"section_id"`
	DocumentID uuid.UUID `json:"document_id"`
	Heading    *string   `json:"heading,omitempty"`
	Content    string    `json:"content"`
	Score      float64   `json:"score"`
	Category   string    `json:"category"`
	Subcategory *string  `json:"subcategory,omitempty"`
	Slug       string    `json:"slug"`
	DocTitle   string    `json:"doc_title"`
}

func (r *SectionRepository) HybridSearch(ctx context.Context, embedding pgvector.Vector, query string, category *string, subcategory *string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	sql := `
		WITH semantic AS (
			SELECT s.id, s.document_id, s.heading, s.content,
				   1 - (s.embedding <=> $1::vector) AS score
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE ($2::text IS NULL OR d.category = $2)
			  AND ($3::text IS NULL OR d.subcategory = $3)
			  AND s.embedding IS NOT NULL
			ORDER BY s.embedding <=> $1::vector
			LIMIT 20
		),
		keyword AS (
			SELECT s.id, ts_rank(s.tsv, plainto_tsquery('english', $4)) AS score
			FROM sections s
			WHERE s.tsv @@ plainto_tsquery('english', $4)
		)
		SELECT sem.id AS section_id, sem.document_id, sem.heading, sem.content,
			   (0.7 * sem.score + 0.3 * COALESCE(kw.score, 0)) AS score,
			   d.category, d.subcategory, d.slug, d.title AS doc_title
		FROM semantic sem
		LEFT JOIN keyword kw ON kw.id = sem.id
		JOIN documents d ON d.id = sem.document_id
		ORDER BY score DESC
		LIMIT $5
	`

	var results []SearchResult
	if err := r.db.WithContext(ctx).Raw(sql, embedding, category, subcategory, query, limit).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	return results, nil
}

func (r *SectionRepository) DeleteByDocumentID(ctx context.Context, docID uuid.UUID) error {
	return r.db.WithContext(ctx).Where("document_id = ?", docID).Delete(&models.Section{}).Error
}

func (r *SectionRepository) CreateBatch(ctx context.Context, sections []models.Section) error {
	if len(sections) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&sections).Error
}

func (r *SectionRepository) Update(ctx context.Context, section *models.Section) error {
	return r.db.WithContext(ctx).Save(section).Error
}

func (r *SectionRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Section, error) {
	var section models.Section
	if err := r.db.WithContext(ctx).Preload("Document").First(&section, id).Error; err != nil {
		return nil, err
	}
	return &section, nil
}
```

**Step 3: Verify build**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add internal/repository/
git commit -m "Add document and section repositories with hybrid search"
```

---

### Task 6: Memory Service (Business Logic + Markdown Parsing)

**Files:**
- Create: `internal/service/memory.go`

**Step 1: Create memory service**

Create `internal/service/memory.go`:

```go
package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

type MemoryService struct {
	docs     *repository.DocumentRepository
	sections *repository.SectionRepository
	embedder *Embedder
}

func NewMemoryService(docs *repository.DocumentRepository, sections *repository.SectionRepository, embedder *Embedder) *MemoryService {
	return &MemoryService{
		docs:     docs,
		sections: sections,
		embedder: embedder,
	}
}

// Search performs hybrid semantic + keyword search.
func (s *MemoryService) Search(ctx context.Context, query string, category, subcategory *string, limit int) ([]repository.SearchResult, error) {
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return s.sections.HybridSearch(ctx, embedding, query, category, subcategory, limit)
}

// GetDocument fetches a document with all sections by path.
func (s *MemoryService) GetDocument(ctx context.Context, category string, subcategory *string, slug string) (*models.Document, error) {
	return s.docs.GetByPath(ctx, category, subcategory, slug)
}

// StoreDocument parses markdown content into sections, embeds them, and stores.
func (s *MemoryService) StoreDocument(ctx context.Context, category string, subcategory *string, slug, content string) (*models.Document, error) {
	title, sections := parseMarkdown(content)
	if title == "" {
		title = slug
	}

	// Upsert the document
	doc := &models.Document{
		Category:    category,
		Subcategory: subcategory,
		Slug:        slug,
		Title:       title,
	}

	// Check if existing
	existing, err := s.docs.GetByPath(ctx, category, subcategory, slug)
	if err == nil {
		doc.ID = existing.ID
		doc.CreatedAt = existing.CreatedAt
		// Delete old sections
		if err := s.sections.DeleteByDocumentID(ctx, existing.ID); err != nil {
			return nil, fmt.Errorf("delete old sections: %w", err)
		}
	}

	if err := s.docs.Upsert(ctx, doc); err != nil {
		return nil, fmt.Errorf("upsert document: %w", err)
	}

	// Embed and create sections
	sectionModels := make([]models.Section, len(sections))
	for i, sec := range sections {
		embedding, err := s.embedder.Embed(ctx, sec.content)
		if err != nil {
			return nil, fmt.Errorf("embed section %d: %w", i, err)
		}
		sectionModels[i] = models.Section{
			DocumentID: doc.ID,
			Ordinal:    i,
			Heading:    sec.heading,
			Content:    sec.content,
			Embedding:  embedding,
		}
	}

	if err := s.sections.CreateBatch(ctx, sectionModels); err != nil {
		return nil, fmt.Errorf("create sections: %w", err)
	}

	doc.Sections = sectionModels
	return doc, nil
}

// UpdateSection updates a single section and re-embeds.
func (s *MemoryService) UpdateSection(ctx context.Context, sectionID uuid.UUID, content string) (*models.Section, error) {
	section, err := s.sections.GetByID(ctx, sectionID)
	if err != nil {
		return nil, fmt.Errorf("get section: %w", err)
	}

	embedding, err := s.embedder.Embed(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("embed section: %w", err)
	}

	section.Content = content
	section.Embedding = embedding

	if err := s.sections.Update(ctx, section); err != nil {
		return nil, fmt.Errorf("update section: %w", err)
	}

	return section, nil
}

// DeleteDocument removes a document and all its sections.
func (s *MemoryService) DeleteDocument(ctx context.Context, category string, subcategory *string, slug string) error {
	return s.docs.DeleteByPath(ctx, category, subcategory, slug)
}

// ListDocuments lists documents, optionally filtered.
func (s *MemoryService) ListDocuments(ctx context.Context, category, subcategory *string) ([]models.Document, error) {
	return s.docs.List(ctx, category, subcategory)
}

// parsedSection holds a parsed markdown section.
type parsedSection struct {
	heading *string
	content string
}

// parseMarkdown splits markdown by ## headings into sections.
// Returns the title (from # heading) and sections.
func parseMarkdown(content string) (string, []parsedSection) {
	lines := strings.Split(content, "\n")
	var title string
	var sections []parsedSection
	var currentHeading *string
	var currentLines []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if text != "" {
			sections = append(sections, parsedSection{
				heading: currentHeading,
				content: text,
			})
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Extract title from # heading (first one only)
		if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ") && title == "" {
			title = strings.TrimPrefix(trimmed, "# ")
			continue
		}

		// Split on ## headings
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			h := strings.TrimPrefix(trimmed, "## ")
			currentHeading = &h
			continue
		}

		currentLines = append(currentLines, line)
	}

	flush()

	// If no sections were created (no ## headings), put everything in one section
	if len(sections) == 0 && strings.TrimSpace(content) != "" {
		sections = append(sections, parsedSection{
			heading: nil,
			content: strings.TrimSpace(content),
		})
	}

	return title, sections
}
```

**Step 2: Verify build**

```bash
go build ./...
```

**Step 3: Commit**

```bash
git add internal/service/memory.go
git commit -m "Add memory service with markdown parsing and embedding"
```

---

### Task 7: MCP Server + Tool Handlers

**Files:**
- Create: `internal/mcp/server.go`
- Create: `internal/mcp/tools.go`

**Step 1: Create MCP server**

Create `internal/mcp/server.go`:

```go
package mcp

import (
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/eliminyro/memory-mcp/internal/service"
)

type Server struct {
	memory *service.MemoryService
	mcp    *mcpsdk.Server
}

func NewServer(memory *service.MemoryService) *Server {
	s := &Server{
		memory: memory,
	}

	impl := &mcpsdk.Implementation{
		Name:    "memory-mcp",
		Version: "1.0.0",
	}

	opts := &mcpsdk.ServerOptions{
		Instructions: `Memory MCP server for persistent context across sessions.

Use search_memory to find relevant memories by semantic similarity and keywords.
Use store_memory to save learnings, preferences, and project state (accepts markdown).
Use get_document to read a specific memory by its hierarchical path.
Use list_documents to browse the memory hierarchy.
Use update_section to modify a single section without rewriting the whole document.
Use delete_document to remove a memory.

Categories: learnings, preferences, projects
Subcategories vary: learnings has go, infrastructure, cicd, etc.`,
	}

	s.mcp = mcpsdk.NewServer(impl, opts)
	s.registerTools()

	return s
}

func (s *Server) HTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.mcp
	}, nil)
}
```

**Step 2: Create tool handlers**

Create `internal/mcp/tools.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerTools() {
	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "search_memory",
		Description: "Search memories using semantic similarity and keyword matching. Returns the most relevant sections across all documents.",
	}, s.SearchMemory)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "get_document",
		Description: "Get a specific document and all its sections by hierarchical path (category/subcategory/slug).",
	}, s.GetDocument)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "store_memory",
		Description: "Store or update a memory document. Accepts markdown content, splits into sections by ## headings, and generates embeddings for semantic search.",
	}, s.StoreMemory)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "update_section",
		Description: "Update a single section's content. Re-generates its embedding automatically.",
	}, s.UpdateSection)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "delete_document",
		Description: "Delete a document and all its sections by path.",
	}, s.DeleteDocument)

	mcpsdk.AddTool(s.mcp, &mcpsdk.Tool{
		Name:        "list_documents",
		Description: "List documents in the memory hierarchy. Filter by category and/or subcategory.",
	}, s.ListDocuments)
}

// --- Input types ---

type SearchMemoryInput struct {
	Query       string  `json:"query" jsonschema:"Search query (required)"`
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category: learnings, preferences, projects"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory: go, infrastructure, hilo, etc."`
	Limit       int     `json:"limit,omitempty" jsonschema:"Max results (default 10)"`
}

type GetDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category (required)"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug (required)"`
}

type StoreMemoryInput struct {
	Category    string  `json:"category" jsonschema:"Document category: learnings, preferences, projects (required)"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory: go, infrastructure, hilo, etc."`
	Slug        string  `json:"slug" jsonschema:"Document slug/filename without extension (required)"`
	Content     string  `json:"content" jsonschema:"Markdown content. Split into sections by ## headings. (required)"`
}

type UpdateSectionInput struct {
	SectionID string `json:"section_id" jsonschema:"Section UUID (required)"`
	Content   string `json:"content" jsonschema:"New section content (required)"`
}

type DeleteDocumentInput struct {
	Category    string  `json:"category" jsonschema:"Document category (required)"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Document subcategory"`
	Slug        string  `json:"slug" jsonschema:"Document slug (required)"`
}

type ListDocumentsInput struct {
	Category    *string `json:"category,omitempty" jsonschema:"Filter by category"`
	Subcategory *string `json:"subcategory,omitempty" jsonschema:"Filter by subcategory"`
}

// --- Handlers ---

func (s *Server) SearchMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input SearchMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Query == "" {
		return errorResult("query is required"), nil, nil
	}
	results, err := s.memory.Search(ctx, input.Query, input.Category, input.Subcategory, input.Limit)
	if err != nil {
		return nil, nil, fmt.Errorf("search: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) GetDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	doc, err := s.memory.GetDocument(ctx, input.Category, input.Subcategory, input.Slug)
	if err != nil {
		return errorResult("document not found: " + err.Error()), nil, nil
	}
	return jsonResult(doc), nil, nil
}

func (s *Server) StoreMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input StoreMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" || input.Content == "" {
		return errorResult("category, slug, and content are required"), nil, nil
	}
	doc, err := s.memory.StoreDocument(ctx, input.Category, input.Subcategory, input.Slug, input.Content)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	return jsonResult(map[string]any{
		"id":       doc.ID,
		"path":     doc.Path(),
		"sections": len(doc.Sections),
	}), nil, nil
}

func (s *Server) UpdateSection(ctx context.Context, _ *mcpsdk.CallToolRequest, input UpdateSectionInput) (*mcpsdk.CallToolResult, any, error) {
	if input.SectionID == "" || input.Content == "" {
		return errorResult("section_id and content are required"), nil, nil
	}
	id, err := uuid.Parse(input.SectionID)
	if err != nil {
		return errorResult("invalid section_id: " + err.Error()), nil, nil
	}
	section, err := s.memory.UpdateSection(ctx, id, input.Content)
	if err != nil {
		return nil, nil, fmt.Errorf("update section: %w", err)
	}
	return jsonResult(section), nil, nil
}

func (s *Server) DeleteDocument(ctx context.Context, _ *mcpsdk.CallToolRequest, input DeleteDocumentInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Category == "" || input.Slug == "" {
		return errorResult("category and slug are required"), nil, nil
	}
	if err := s.memory.DeleteDocument(ctx, input.Category, input.Subcategory, input.Slug); err != nil {
		return errorResult("delete failed: " + err.Error()), nil, nil
	}
	return jsonResult(map[string]string{"status": "deleted"}), nil, nil
}

func (s *Server) ListDocuments(ctx context.Context, _ *mcpsdk.CallToolRequest, input ListDocumentsInput) (*mcpsdk.CallToolResult, any, error) {
	docs, err := s.memory.ListDocuments(ctx, input.Category, input.Subcategory)
	if err != nil {
		return nil, nil, fmt.Errorf("list: %w", err)
	}
	// Return compact listing
	type docEntry struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	entries := make([]docEntry, len(docs))
	for i, d := range docs {
		entries[i] = docEntry{Path: d.Path(), Title: d.Title}
	}
	return jsonResult(entries), nil, nil
}

// --- Helpers ---

func errorResult(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: msg},
		},
	}
}

func jsonResult(v any) *mcpsdk.CallToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult("marshal error: " + err.Error())
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: string(data)},
		},
	}
}
```

**Step 3: Verify build**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add internal/mcp/
git commit -m "Add MCP server with 6 tool handlers"
```

---

### Task 8: Wire Everything in main.go

**Files:**
- Modify: `cmd/server/main.go`

**Step 1: Complete main.go**

Update `cmd/server/main.go`:

```go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eliminyro/memory-mcp/internal/config"
	"github.com/eliminyro/memory-mcp/internal/database"
	"github.com/eliminyro/memory-mcp/internal/mcp"
	"github.com/eliminyro/memory-mcp/internal/repository"
	"github.com/eliminyro/memory-mcp/internal/service"
)

func main() {
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

	if err := database.Migrate(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	// Repositories
	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)

	// Services
	embedder := service.NewEmbedder(cfg.OllamaURL, cfg.OllamaModel)
	memorySvc := service.NewMemoryService(docRepo, sectionRepo, embedder)

	// MCP server
	mcpServer := mcp.NewServer(memorySvc)

	// HTTP server
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpServer.HTTPHandler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         cfg.ServerAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("memory-mcp listening", "addr", cfg.ServerAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
```

**Step 2: Verify build**

```bash
go build ./cmd/server
```

**Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "Wire all dependencies in main.go with graceful shutdown"
```

---

### Task 9: Import CLI

**Files:**
- Create: `cmd/import/main.go`

**Step 1: Create import tool**

Create `cmd/import/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/eliminyro/memory-mcp/internal/config"
	"github.com/eliminyro/memory-mcp/internal/database"
	"github.com/eliminyro/memory-mcp/internal/repository"
	"github.com/eliminyro/memory-mcp/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: import <memory-directory>\n")
		fmt.Fprintf(os.Stderr, "Example: import ~/.claude/context/memory\n")
		os.Exit(1)
	}

	memoryDir := os.Args[1]

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

	if err := database.Migrate(db); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)
	embedder := service.NewEmbedder(cfg.OllamaURL, cfg.OllamaModel)
	memorySvc := service.NewMemoryService(docRepo, sectionRepo, embedder)

	ctx := context.Background()
	var imported, failed int

	err = filepath.Walk(memoryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if strings.Contains(path, "CLAUDE.md") {
			return nil // Skip CLAUDE.md instruction files
		}

		rel, _ := filepath.Rel(memoryDir, path)
		category, subcategory, slug := parsePath(rel)

		if category == "" || slug == "" {
			slog.Warn("skipping unparseable path", "path", rel)
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			slog.Error("failed to read file", "path", path, "error", err)
			failed++
			return nil
		}

		slog.Info("importing", "path", rel, "category", category, "subcategory", subcategory, "slug", slug)

		_, err = memorySvc.StoreDocument(ctx, category, subcategory, slug, string(content))
		if err != nil {
			slog.Error("failed to import", "path", rel, "error", err)
			failed++
			return nil
		}

		imported++
		return nil
	})

	if err != nil {
		slog.Error("walk error", "error", err)
		os.Exit(1)
	}

	slog.Info("import complete", "imported", imported, "failed", failed)
}

// parsePath converts "learnings/go/gorm.md" → ("learnings", "go", "gorm")
// and "preferences/workflow.md" → ("preferences", nil, "workflow")
// and "work-status.md" → ("", nil, "") — skipped
func parsePath(rel string) (category string, subcategory *string, slug string) {
	rel = strings.TrimSuffix(rel, ".md")
	parts := strings.Split(filepath.ToSlash(rel), "/")

	switch len(parts) {
	case 3:
		// learnings/go/gorm
		sub := parts[1]
		return parts[0], &sub, parts[2]
	case 2:
		// preferences/workflow or projects/hilo (but hilo is actually projects/hilo/state)
		return parts[0], nil, parts[1]
	case 1:
		// work-status (top-level)
		return "misc", nil, parts[0]
	default:
		return "", nil, ""
	}
}
```

**Step 2: Verify build**

```bash
go build ./cmd/import
```

**Step 3: Commit**

```bash
git add cmd/import/
git commit -m "Add one-time import CLI for migrating file-based memory"
```

---

### Task 10: End-to-End Test

**Step 1: Start infrastructure**

```bash
cd ~/mystuff/goprojects/memory-mcp
docker compose up -d postgres ollama
```

**Step 2: Pull embedding model**

```bash
docker compose exec ollama ollama pull nomic-embed-text
```

**Step 3: Run the server locally**

```bash
go run ./cmd/server
```

Expected: "memory-mcp listening addr=:8080"

**Step 4: Test via curl** (MCP uses streamable HTTP, but health endpoint is plain HTTP)

```bash
curl http://localhost:8080/health
```

Expected: `ok`

**Step 5: Run the import**

```bash
go run ./cmd/import ~/.claude/context/memory
```

Expected: logs showing each file imported, final "import complete imported=N failed=0"

**Step 6: Commit any fixes**

```bash
git add -A
git commit -m "Fix issues found during end-to-end testing"
```

---

### Task 11: Docker Build + Compose Verification

**Step 1: Build Docker image**

```bash
docker compose build memory-mcp
```

**Step 2: Run full stack**

```bash
docker compose up
```

Expected: All 3 services start, memory-mcp connects to postgres, health check passes.

**Step 3: Commit any Dockerfile fixes**

```bash
git add -A
git commit -m "Fix Docker build issues"
```
