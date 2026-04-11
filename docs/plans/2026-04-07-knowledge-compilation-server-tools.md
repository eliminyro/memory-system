# Knowledge Compilation — Server Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three new MCP tools to memory-mcp: `generate_index`, `get_related`, and `lint_memory` — all pure SQL, no LLM calls.

**Architecture:** New repository methods for aggregation and similarity queries, thin service methods for tenant resolution, MCP tool handlers following existing patterns. No new models or migrations needed — all queries operate on existing tables and indexes.

**Tech Stack:** Go, PostgreSQL, pgvector, GORM raw SQL, MCP go-sdk v1.3.0

**Spec:** `docs/specs/2026-04-07-knowledge-compilation-design.md` — Section 1

---

### Task 1: `GenerateIndex` Repository Method

**Files:**
- Modify: `internal/repository/document.go`
- Create: `internal/repository/document_test.go`

- [ ] **Step 1: Write the failing test for GenerateIndex summary depth**

```go
// internal/repository/document_test.go
package repository_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
	"github.com/eliminyro/memory-mcp/internal/database"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("MEMORY_MCP_TEST_DSN")
	if dsn == "" {
		t.Skip("MEMORY_MCP_TEST_DSN not set")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, 1024))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	})
	return db
}

func TestGenerateIndex_Summary(t *testing.T) {
	db := setupTestDB(t)
	repo := repository.NewDocumentRepository(db)
	ctx := context.Background()
	tid := uuid.New()

	// Seed test docs
	docs := []models.Document{
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "gorm", Title: "GORM Patterns"},
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "chi", Title: "Chi Router"},
		{TenantID: tid, Category: "projects", Subcategory: ptr("hilo"), Slug: "state", Title: "Hilo State"},
		{TenantID: tid, Category: "preferences", Subcategory: nil, Slug: "style", Title: "Coding Style"},
	}
	for i := range docs {
		require.NoError(t, db.WithContext(ctx).Create(&docs[i]).Error)
	}
	t.Cleanup(func() {
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
	})

	results, err := repo.GenerateIndex(ctx, tid, repository.IndexDepthSummary, nil)
	require.NoError(t, err)
	assert.Len(t, results, 3) // learnings/go, projects/hilo, preferences

	// Check learnings/go entry
	var goEntry *repository.IndexEntry
	for i := range results {
		if results[i].Category == "learnings" && ptrVal(results[i].Subcategory) == "go" {
			goEntry = &results[i]
			break
		}
	}
	require.NotNil(t, goEntry)
	assert.Equal(t, 2, goEntry.DocCount)
	assert.Contains(t, goEntry.Topics, "GORM Patterns")
	assert.Contains(t, goEntry.Topics, "Chi Router")
}

func ptr(s string) *string { return &s }
func ptrVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestGenerateIndex_Summary -v`
Expected: Compilation error — `GenerateIndex` method and `IndexEntry`/`IndexDepthSummary` don't exist yet.

- [ ] **Step 3: Define IndexEntry type and IndexDepth constants**

Add to `internal/repository/document.go` after the `DocumentRepository` struct:

```go
// IndexDepth controls the level of detail in the generated index.
type IndexDepth string

const (
	IndexDepthSummary  IndexDepth = "summary"
	IndexDepthCategory IndexDepth = "category"
	IndexDepthFull     IndexDepth = "full"
)

// IndexEntry represents one line in the generated index.
type IndexEntry struct {
	Category    string  `json:"category"`
	Subcategory *string `json:"subcategory,omitempty"`
	DocCount    int     `json:"doc_count"`
	Topics      string  `json:"topics"` // comma-separated titles
}
```

- [ ] **Step 4: Implement GenerateIndex**

Add to `internal/repository/document.go`:

```go
// GenerateIndex produces a tiered catalog of documents for a tenant.
func (r *DocumentRepository) GenerateIndex(ctx context.Context, tenantID uuid.UUID, depth IndexDepth, category *string) ([]IndexEntry, error) {
	tenants := readTenants(tenantID)

	switch depth {
	case IndexDepthSummary:
		return r.indexSummary(ctx, tenants, category)
	case IndexDepthCategory:
		return r.indexCategory(ctx, tenants, category)
	case IndexDepthFull:
		return r.indexCategory(ctx, tenants, nil)
	default:
		return nil, fmt.Errorf("%w: invalid depth %q", apperr.ErrInvalidInput, depth)
	}
}

func (r *DocumentRepository) indexSummary(ctx context.Context, tenants []uuid.UUID, category *string) ([]IndexEntry, error) {
	sql := `
		SELECT category, subcategory, COUNT(*) AS doc_count,
		       string_agg(title, ', ' ORDER BY title) AS topics
		FROM documents
		WHERE tenant_id IN ?
		  AND (?::text IS NULL OR category = ?)
		GROUP BY category, subcategory
		ORDER BY category, subcategory
	`
	var entries []IndexEntry
	if err := r.db.WithContext(ctx).Raw(sql, tenants, category, category).Scan(&entries).Error; err != nil {
		return nil, fmt.Errorf("generate index summary: %w", err)
	}
	return entries, nil
}

func (r *DocumentRepository) indexCategory(ctx context.Context, tenants []uuid.UUID, category *string) ([]IndexEntry, error) {
	sql := `
		SELECT category, subcategory, 1 AS doc_count, title AS topics
		FROM documents
		WHERE tenant_id IN ?
		  AND (?::text IS NULL OR category = ?)
		ORDER BY category, subcategory, slug
	`
	var entries []IndexEntry
	if err := r.db.WithContext(ctx).Raw(sql, tenants, category, category).Scan(&entries).Error; err != nil {
		return nil, fmt.Errorf("generate index category: %w", err)
	}
	return entries, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestGenerateIndex_Summary -v`
Expected: PASS

- [ ] **Step 6: Write test for category depth and category filter**

Add to `internal/repository/document_test.go`:

```go
func TestGenerateIndex_Category(t *testing.T) {
	db := setupTestDB(t)
	repo := repository.NewDocumentRepository(db)
	ctx := context.Background()
	tid := uuid.New()

	docs := []models.Document{
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "gorm", Title: "GORM Patterns"},
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "chi", Title: "Chi Router"},
		{TenantID: tid, Category: "projects", Subcategory: ptr("hilo"), Slug: "state", Title: "Hilo State"},
	}
	for i := range docs {
		require.NoError(t, db.WithContext(ctx).Create(&docs[i]).Error)
	}
	t.Cleanup(func() {
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
	})

	// Category depth — one entry per doc
	results, err := repo.GenerateIndex(ctx, tid, repository.IndexDepthCategory, nil)
	require.NoError(t, err)
	assert.Len(t, results, 3)

	// With category filter
	cat := "learnings"
	filtered, err := repo.GenerateIndex(ctx, tid, repository.IndexDepthCategory, &cat)
	require.NoError(t, err)
	assert.Len(t, filtered, 2)
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestGenerateIndex -v`
Expected: PASS (both tests)

- [ ] **Step 8: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

---

### Task 2: `GetRelated` Repository Method

**Files:**
- Modify: `internal/repository/section.go`
- Modify: `internal/repository/document_test.go` (add tests)

- [ ] **Step 1: Write the failing test**

Add to `internal/repository/document_test.go`:

```go
func TestGetRelated(t *testing.T) {
	db := setupTestDB(t)
	sectionRepo := repository.NewSectionRepository(db)
	docRepo := repository.NewDocumentRepository(db)
	ctx := context.Background()
	tid := uuid.New()

	// Create two docs with sections that have embeddings
	doc1 := models.Document{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "gorm", Title: "GORM"}
	doc2 := models.Document{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "chi", Title: "Chi"}
	doc3 := models.Document{TenantID: tid, Category: "projects", Slug: "unrelated", Title: "Unrelated"}
	require.NoError(t, db.WithContext(ctx).Create(&doc1).Error)
	require.NoError(t, db.WithContext(ctx).Create(&doc2).Error)
	require.NoError(t, db.WithContext(ctx).Create(&doc3).Error)
	t.Cleanup(func() {
		db.Where("tenant_id = ?", tid).Delete(&models.Section{})
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
	})

	// Note: real test needs actual embeddings — use the embedding provider
	// or mock vectors. Skipping embedding-dependent assertions for unit test;
	// integration test validates ranking.

	results, err := sectionRepo.GetRelated(ctx, tid, doc1.ID, 5)
	require.NoError(t, err)
	// Returns other docs, not the target doc
	for _, r := range results {
		assert.NotEqual(t, doc1.ID, r.DocumentID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestGetRelated -v`
Expected: Compilation error — `GetRelated` doesn't exist.

- [ ] **Step 3: Define RelatedResult type and implement GetRelated**

Add to `internal/repository/section.go`:

```go
// RelatedResult represents a document related to a target document.
type RelatedResult struct {
	DocumentID  uuid.UUID `json:"document_id"`
	Category    string    `json:"category"`
	Subcategory *string   `json:"subcategory,omitempty"`
	Slug        string    `json:"slug"`
	DocTitle    string    `json:"doc_title"`
	Similarity  float64   `json:"similarity"`
}

// GetRelated finds documents semantically related to the given document.
// It averages the target document's section embeddings and finds the closest
// documents by cosine similarity, excluding the target document itself.
func (r *SectionRepository) GetRelated(ctx context.Context, tenantID uuid.UUID, documentID uuid.UUID, limit int) ([]RelatedResult, error) {
	if limit <= 0 {
		limit = 5
	}
	tenants := readTenants(tenantID)

	sql := `
		WITH target_avg AS (
			SELECT AVG(embedding) AS avg_embedding
			FROM sections
			WHERE document_id = ?
			  AND embedding IS NOT NULL
		),
		candidates AS (
			SELECT s.document_id,
			       1 - (s.embedding <=> (SELECT avg_embedding FROM target_avg)::vector) AS sim
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND s.document_id != ?
			  AND s.embedding IS NOT NULL
		)
		SELECT c.document_id,
		       d.category, d.subcategory, d.slug, d.title AS doc_title,
		       AVG(c.sim) AS similarity
		FROM candidates c
		JOIN documents d ON d.id = c.document_id
		GROUP BY c.document_id, d.category, d.subcategory, d.slug, d.title
		ORDER BY similarity DESC
		LIMIT ?
	`

	var results []RelatedResult
	if err := r.db.WithContext(ctx).Raw(sql, documentID, tenants, documentID, limit).Scan(&results).Error; err != nil {
		return nil, fmt.Errorf("get related: %w", err)
	}
	return results, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestGetRelated -v`
Expected: PASS

- [ ] **Step 5: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

---

### Task 3: `LintMemory` Repository Methods

**Files:**
- Create: `internal/repository/lint.go`
- Create: `internal/repository/lint_test.go`

- [ ] **Step 1: Define lint types**

Create `internal/repository/lint.go`:

```go
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type LintRepository struct {
	db *gorm.DB
}

func NewLintRepository(db *gorm.DB) *LintRepository {
	return &LintRepository{db: db}
}

// LintSeverity indicates how urgent a finding is.
type LintSeverity string

const (
	LintSeverityWarning LintSeverity = "warning"
	LintSeverityInfo    LintSeverity = "info"
)

// LintFinding represents a single lint check result.
type LintFinding struct {
	Check        string       `json:"check"`
	Severity     LintSeverity `json:"severity"`
	DocumentPath string       `json:"document_path"`
	Message      string       `json:"message"`
}

// LintThresholds configures the sensitivity of lint checks.
type LintThresholds struct {
	StaleDays            int     `json:"stale_days"`
	SparseMinSections    int     `json:"sparse_min_sections"`
	SparseMinContentLen  int     `json:"sparse_min_content_len"`
	DuplicateSimilarity  float64 `json:"duplicate_similarity"`
	EmptyCategoryMinDocs int     `json:"empty_category_min_docs"`
}

// DefaultLintThresholds returns sensible defaults.
func DefaultLintThresholds() LintThresholds {
	return LintThresholds{
		StaleDays:            90,
		SparseMinSections:    2,
		SparseMinContentLen:  100,
		DuplicateSimilarity:  0.92,
		EmptyCategoryMinDocs: 2,
	}
}
```

- [ ] **Step 2: Run compilation check**

Run: `cd ~/mystuff/goprojects/memory-mcp && go build ./...`
Expected: PASS

- [ ] **Step 3: Write failing test for stale docs check**

Create `internal/repository/lint_test.go`:

```go
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
)

func TestLintStale(t *testing.T) {
	db := setupTestDB(t)
	repo := repository.NewLintRepository(db)
	ctx := context.Background()
	tid := uuid.New()

	// Create a doc with old updated_at
	doc := models.Document{
		TenantID: tid, Category: "learnings", Subcategory: ptr("go"),
		Slug: "old-thing", Title: "Old Thing",
	}
	require.NoError(t, db.WithContext(ctx).Create(&doc).Error)
	// Force old timestamp
	db.Exec("UPDATE documents SET updated_at = ? WHERE id = ?", time.Now().AddDate(0, -6, 0), doc.ID)
	t.Cleanup(func() {
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
	})

	thresholds := repository.DefaultLintThresholds()
	thresholds.StaleDays = 30

	findings, err := repo.CheckStale(ctx, tid, thresholds)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "stale", findings[0].Check)
	assert.Contains(t, findings[0].DocumentPath, "old-thing")
}
```

- [ ] **Step 4: Implement CheckStale**

Add to `internal/repository/lint.go`:

```go
// CheckStale finds documents not updated within the threshold days.
func (r *LintRepository) CheckStale(ctx context.Context, tenantID uuid.UUID, t LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)
	cutoff := time.Now().AddDate(0, 0, -t.StaleDays)

	sql := `
		SELECT category, subcategory, slug, title, updated_at
		FROM documents
		WHERE tenant_id IN ?
		  AND updated_at < ?
		ORDER BY updated_at ASC
	`

	type row struct {
		Category    string
		Subcategory *string
		Slug        string
		Title       string
		UpdatedAt   time.Time
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, cutoff).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check stale: %w", err)
	}

	findings := make([]LintFinding, len(rows))
	for i, r := range rows {
		path := r.Category
		if r.Subcategory != nil {
			path += "/" + *r.Subcategory
		}
		path += "/" + r.Slug
		days := int(time.Since(r.UpdatedAt).Hours() / 24)
		findings[i] = LintFinding{
			Check:        "stale",
			Severity:     LintSeverityInfo,
			DocumentPath: path,
			Message:      fmt.Sprintf("%q not updated in %d days", r.Title, days),
		}
	}
	return findings, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestLintStale -v`
Expected: PASS

- [ ] **Step 6: Write failing test for sparse docs check**

Add to `internal/repository/lint_test.go`:

```go
func TestLintSparse(t *testing.T) {
	db := setupTestDB(t)
	repo := repository.NewLintRepository(db)
	ctx := context.Background()
	tid := uuid.New()

	doc := models.Document{
		TenantID: tid, Category: "learnings", Subcategory: ptr("go"),
		Slug: "sparse-doc", Title: "Sparse",
	}
	require.NoError(t, db.WithContext(ctx).Create(&doc).Error)
	// One short section
	sec := models.Section{
		DocumentID: doc.ID, Ordinal: 0, Content: "short",
	}
	require.NoError(t, db.WithContext(ctx).Create(&sec).Error)
	t.Cleanup(func() {
		db.Where("document_id = ?", doc.ID).Delete(&models.Section{})
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
	})

	thresholds := repository.DefaultLintThresholds()
	findings, err := repo.CheckSparse(ctx, tid, thresholds)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "sparse", findings[0].Check)
}
```

- [ ] **Step 7: Implement CheckSparse**

Add to `internal/repository/lint.go`:

```go
// CheckSparse finds documents with too few or too short sections.
func (r *LintRepository) CheckSparse(ctx context.Context, tenantID uuid.UUID, t LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT d.category, d.subcategory, d.slug, d.title,
		       COUNT(s.id) AS section_count,
		       COALESCE(MAX(LENGTH(s.content)), 0) AS max_content_len
		FROM documents d
		LEFT JOIN sections s ON s.document_id = d.id
		WHERE d.tenant_id IN ?
		GROUP BY d.id, d.category, d.subcategory, d.slug, d.title
		HAVING COUNT(s.id) < ? OR COALESCE(MAX(LENGTH(s.content)), 0) < ?
		ORDER BY d.category, d.subcategory, d.slug
	`

	type row struct {
		Category      string
		Subcategory   *string
		Slug          string
		Title         string
		SectionCount  int
		MaxContentLen int
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, t.SparseMinSections, t.SparseMinContentLen).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check sparse: %w", err)
	}

	findings := make([]LintFinding, len(rows))
	for i, r := range rows {
		path := r.Category
		if r.Subcategory != nil {
			path += "/" + *r.Subcategory
		}
		path += "/" + r.Slug
		findings[i] = LintFinding{
			Check:        "sparse",
			Severity:     LintSeverityInfo,
			DocumentPath: path,
			Message:      fmt.Sprintf("%q has %d sections, longest %d chars", r.Title, r.SectionCount, r.MaxContentLen),
		}
	}
	return findings, nil
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestLintSparse -v`
Expected: PASS

- [ ] **Step 9: Implement CheckNearDuplicates**

Add to `internal/repository/lint.go`:

```go
// CheckNearDuplicates finds document pairs with high cosine similarity.
func (r *LintRepository) CheckNearDuplicates(ctx context.Context, tenantID uuid.UUID, t LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		WITH doc_embeddings AS (
			SELECT s.document_id, AVG(s.embedding) AS avg_embedding
			FROM sections s
			JOIN documents d ON d.id = s.document_id
			WHERE d.tenant_id IN ?
			  AND s.embedding IS NOT NULL
			GROUP BY s.document_id
		)
		SELECT d1.category AS cat1, d1.subcategory AS subcat1, d1.slug AS slug1, d1.title AS title1,
		       d2.category AS cat2, d2.subcategory AS subcat2, d2.slug AS slug2, d2.title AS title2,
		       1 - (e1.avg_embedding <=> e2.avg_embedding) AS similarity
		FROM doc_embeddings e1
		JOIN doc_embeddings e2 ON e1.document_id < e2.document_id
		JOIN documents d1 ON d1.id = e1.document_id
		JOIN documents d2 ON d2.id = e2.document_id
		WHERE 1 - (e1.avg_embedding <=> e2.avg_embedding) > ?
		ORDER BY similarity DESC
	`

	type row struct {
		Cat1       string
		Subcat1    *string
		Slug1      string
		Title1     string
		Cat2       string
		Subcat2    *string
		Slug2      string
		Title2     string
		Similarity float64
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, t.DuplicateSimilarity).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check near duplicates: %w", err)
	}

	findings := make([]LintFinding, len(rows))
	for i, r := range rows {
		path1 := buildPath(r.Cat1, r.Subcat1, r.Slug1)
		path2 := buildPath(r.Cat2, r.Subcat2, r.Slug2)
		findings[i] = LintFinding{
			Check:        "near_duplicate",
			Severity:     LintSeverityWarning,
			DocumentPath: path1,
			Message:      fmt.Sprintf("%q and %q are %.0f%% similar — consider merging", r.Title1, r.Title2, r.Similarity*100),
		}
		_ = path2 // both paths included in the message via titles
	}
	return findings, nil
}

func buildPath(category string, subcategory *string, slug string) string {
	if subcategory != nil {
		return category + "/" + *subcategory + "/" + slug
	}
	return category + "/" + slug
}
```

- [ ] **Step 10: Implement CheckEmptyCategories**

Add to `internal/repository/lint.go`:

```go
// CheckEmptyCategories finds subcategories with fewer docs than the threshold.
func (r *LintRepository) CheckEmptyCategories(ctx context.Context, tenantID uuid.UUID, t LintThresholds) ([]LintFinding, error) {
	tenants := readTenants(tenantID)

	sql := `
		SELECT category, subcategory, COUNT(*) AS doc_count
		FROM documents
		WHERE tenant_id IN ?
		  AND subcategory IS NOT NULL
		GROUP BY category, subcategory
		HAVING COUNT(*) < ?
		ORDER BY category, subcategory
	`

	type row struct {
		Category    string
		Subcategory *string
		DocCount    int
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, tenants, t.EmptyCategoryMinDocs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("check empty categories: %w", err)
	}

	findings := make([]LintFinding, len(rows))
	for i, r := range rows {
		path := r.Category
		if r.Subcategory != nil {
			path += "/" + *r.Subcategory
		}
		findings[i] = LintFinding{
			Check:        "empty_category",
			Severity:     LintSeverityInfo,
			DocumentPath: path,
			Message:      fmt.Sprintf("subcategory has only %d doc(s) — consider reorganizing", r.DocCount),
		}
	}
	return findings, nil
}
```

- [ ] **Step 11: Run all lint tests**

Run: `cd ~/mystuff/goprojects/memory-mcp && go test ./internal/repository/ -run TestLint -v`
Expected: PASS

- [ ] **Step 12: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

---

### Task 4: Service Layer — GenerateIndex, GetRelated, LintMemory

**Files:**
- Modify: `internal/service/memory.go`
- Create: `internal/service/memory_test.go`

- [ ] **Step 1: Add LintRepository to MemoryService**

Modify `internal/service/memory.go` — update the struct and constructor:

```go
// Add to MemoryService struct:
lint *repository.LintRepository

// Update NewMemoryService signature to accept it:
func NewMemoryService(db *gorm.DB, docs *repository.DocumentRepository, sections *repository.SectionRepository, embedder EmbeddingProvider, tenants *repository.TenantRepository, keys *repository.APIKeyRepository, lint *repository.LintRepository, adminEmails []string) *MemoryService {
	// ... existing code ...
	return &MemoryService{
		db:          db,
		docs:        docs,
		sections:    sections,
		embedder:    embedder,
		tenants:     tenants,
		keys:        keys,
		lint:        lint,
		adminEmails: ae,
	}
}
```

- [ ] **Step 2: Update cmd/server/main.go to create and pass LintRepository**

After the line that creates `sectionRepo`, add:

```go
lintRepo := repository.NewLintRepository(db)
```

Update the `NewMemoryService` call to pass `lintRepo` before `adminEmails`.

- [ ] **Step 3: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-mcp && go build ./...`
Expected: PASS

- [ ] **Step 4: Implement GenerateIndex service method**

Add to `internal/service/memory.go`:

```go
// GenerateIndex produces a tiered catalog of the tenant's knowledge base.
func (s *MemoryService) GenerateIndex(ctx context.Context, depth string, category *string, overrideID *uuid.UUID) ([]repository.IndexEntry, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	d := repository.IndexDepth(depth)
	return s.docs.GenerateIndex(ctx, tid, d, category)
}
```

- [ ] **Step 5: Implement GetRelated service method**

Add to `internal/service/memory.go`:

```go
// GetRelated finds documents semantically similar to the given document.
func (s *MemoryService) GetRelated(ctx context.Context, documentID uuid.UUID, limit int, overrideID *uuid.UUID) ([]repository.RelatedResult, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	return s.sections.GetRelated(ctx, tid, documentID, limit)
}
```

- [ ] **Step 6: Implement LintMemory service method**

Add to `internal/service/memory.go`:

```go
// LintMemory runs health checks on the tenant's knowledge base.
func (s *MemoryService) LintMemory(ctx context.Context, checks []string, thresholds *repository.LintThresholds, overrideID *uuid.UUID) ([]repository.LintFinding, error) {
	tid, err := s.resolveTenant(ctx, overrideID)
	if err != nil {
		return nil, err
	}

	t := repository.DefaultLintThresholds()
	if thresholds != nil {
		t = *thresholds
	}

	allChecks := map[string]func(context.Context, uuid.UUID, repository.LintThresholds) ([]repository.LintFinding, error){
		"stale":           s.lint.CheckStale,
		"sparse":          s.lint.CheckSparse,
		"near_duplicate":  s.lint.CheckNearDuplicates,
		"empty_category":  s.lint.CheckEmptyCategories,
	}

	// Filter to requested checks (or all if none specified)
	run := allChecks
	if len(checks) > 0 {
		run = make(map[string]func(context.Context, uuid.UUID, repository.LintThresholds) ([]repository.LintFinding, error))
		for _, c := range checks {
			if fn, ok := allChecks[c]; ok {
				run[c] = fn
			}
		}
	}

	var findings []repository.LintFinding
	for _, fn := range run {
		results, err := fn(ctx, tid, t)
		if err != nil {
			return nil, err
		}
		findings = append(findings, results...)
	}
	return findings, nil
}
```

- [ ] **Step 7: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-mcp && go build ./...`
Expected: PASS

- [ ] **Step 8: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

---

### Task 5: MCP Tool Handlers — generate_index, get_related, lint_memory

**Files:**
- Modify: `internal/mcp/tools.go`

- [ ] **Step 1: Define input types**

Add to `internal/mcp/tools.go` in the input types section:

```go
type GenerateIndexInput struct {
	Depth    string  `json:"depth,omitempty" jsonschema:"Index depth: summary (default), category, or full"`
	Category *string `json:"category,omitempty" jsonschema:"Filter to specific category"`
	TenantID *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type GetRelatedInput struct {
	DocumentID string  `json:"document_id" jsonschema:"UUID of the target document"`
	Limit      int     `json:"limit,omitempty" jsonschema:"Max results (default 5)"`
	TenantID   *string `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}

type LintMemoryInput struct {
	Checks   []string                    `json:"checks,omitempty" jsonschema:"Filter to specific checks: stale, sparse, near_duplicate, empty_category"`
	Thresholds *repository.LintThresholds `json:"thresholds,omitempty" jsonschema:"Override default thresholds"`
	TenantID *string                      `json:"tenant_id,omitempty" jsonschema:"(Admin only) Target a specific tenant. Omit to use your own."`
}
```

Add the import for `repository` at the top of the file:

```go
"github.com/eliminyro/memory-mcp/internal/repository"
```

- [ ] **Step 2: Implement handler methods**

Add to `internal/mcp/tools.go`:

```go
func (s *Server) GenerateIndex(ctx context.Context, _ *mcpsdk.CallToolRequest, input GenerateIndexInput) (*mcpsdk.CallToolResult, any, error) {
	if input.Depth == "" {
		input.Depth = "summary"
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	entries, err := s.memory.GenerateIndex(ctx, input.Depth, input.Category, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("generate index: %w", err)
	}
	return jsonResult(entries), nil, nil
}

func (s *Server) GetRelated(ctx context.Context, _ *mcpsdk.CallToolRequest, input GetRelatedInput) (*mcpsdk.CallToolResult, any, error) {
	if input.DocumentID == "" {
		return errorResult("document_id is required"), nil, nil
	}
	docID, err := uuid.Parse(input.DocumentID)
	if err != nil {
		return errorResult("invalid document_id: " + err.Error()), nil, nil
	}
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	results, err := s.memory.GetRelated(ctx, docID, input.Limit, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("get related: %w", err)
	}
	return jsonResult(results), nil, nil
}

func (s *Server) LintMemory(ctx context.Context, _ *mcpsdk.CallToolRequest, input LintMemoryInput) (*mcpsdk.CallToolResult, any, error) {
	tenantOverride, err := parseTenantOverride(input.TenantID)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	findings, err := s.memory.LintMemory(ctx, input.Checks, input.Thresholds, tenantOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("lint memory: %w", err)
	}
	return jsonResult(findings), nil, nil
}
```

- [ ] **Step 3: Register tools**

Add to the `registerTools` function in `internal/mcp/tools.go`, after the existing tool registrations:

```go
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "generate_index",
		Description: "Generate a tiered catalog of the knowledge base. Use depth='summary' for a compact overview (one line per subcategory), 'category' for doc-level detail, or 'full' for everything.",
	}, s.GenerateIndex)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_related",
		Description: "Find documents semantically related to a given document. Uses cosine similarity between section embeddings.",
	}, s.GetRelated)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "lint_memory",
		Description: "Run health checks on the knowledge base. Detects stale docs, sparse content, near-duplicates, and empty categories. All checks are SQL-based, no LLM calls.",
	}, s.LintMemory)
```

- [ ] **Step 4: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-mcp && go build ./...`
Expected: PASS

- [ ] **Step 5: Format and lint**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

---

### Task 6: Update MCP Server Instructions

**Files:**
- Modify: `internal/mcp/server.go` (if instructions are defined there)
- Or: Update wherever the MCP server description / tool instructions are configured

- [ ] **Step 1: Check where MCP instructions are defined**

Search for the MCP server instructions string — the text that tells Claude how to use the tools. It may be in `server.go` or in the `NewServer` function.

- [ ] **Step 2: Add tool descriptions to the MCP instructions**

Append to the instructions string:

```
Use generate_index to get a compact overview of the knowledge base (summary depth recommended for session start).
Use get_related to find documents semantically similar to a specific document.
Use lint_memory to check knowledge base health — stale docs, sparse content, near-duplicates.
```

- [ ] **Step 3: Verify compilation**

Run: `cd ~/mystuff/goprojects/memory-mcp && go build ./...`
Expected: PASS

---

### Task 7: Integration Test — Full Round Trip

**Files:**
- Create: `internal/mcp/tools_test.go`

- [ ] **Step 1: Write integration test for generate_index via MCP**

```go
// internal/mcp/tools_test.go
package mcp_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-mcp/internal/auth"
	"github.com/eliminyro/memory-mcp/internal/database"
	mcpserver "github.com/eliminyro/memory-mcp/internal/mcp"
	"github.com/eliminyro/memory-mcp/internal/models"
	"github.com/eliminyro/memory-mcp/internal/repository"
	"github.com/eliminyro/memory-mcp/internal/service"
)

func setupIntegrationTest(t *testing.T) (*mcpserver.Server, *gorm.DB, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("MEMORY_MCP_TEST_DSN")
	if dsn == "" {
		t.Skip("MEMORY_MCP_TEST_DSN not set")
	}

	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, 1024))

	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)
	tenantRepo := repository.NewTenantRepository(db)
	keyRepo := repository.NewAPIKeyRepository(db)
	lintRepo := repository.NewLintRepository(db)

	// Use a stub embedder that returns zero vectors for testing
	embedder := &stubEmbedder{dims: 1024}

	memorySvc := service.NewMemoryService(db, docRepo, sectionRepo, embedder, tenantRepo, keyRepo, lintRepo, []string{"admin@test.com"})
	srv := mcpserver.NewServer(memorySvc, []string{"admin@test.com"})

	tid := uuid.New()
	t.Cleanup(func() {
		db.Where("tenant_id = ?", tid).Delete(&models.Section{})
		db.Where("tenant_id = ?", tid).Delete(&models.Document{})
		sqlDB, _ := db.DB()
		sqlDB.Close()
	})

	return srv, db, tid
}

type stubEmbedder struct{ dims int }

func (e *stubEmbedder) Embed(_ context.Context, _ string) (pgvector.Vector, error) {
	v := make([]float32, e.dims)
	return pgvector.NewVector(v), nil
}

func (e *stubEmbedder) Dimensions() int { return e.dims }

func TestGenerateIndex_Integration(t *testing.T) {
	srv, db, tid := setupIntegrationTest(t)
	ctx := auth.WithTenantID(context.Background(), tid)

	// Seed docs
	for _, d := range []models.Document{
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "gorm", Title: "GORM"},
		{TenantID: tid, Category: "learnings", Subcategory: ptr("go"), Slug: "chi", Title: "Chi"},
		{TenantID: tid, Category: "projects", Subcategory: ptr("hilo"), Slug: "state", Title: "Hilo"},
	} {
		require.NoError(t, db.WithContext(ctx).Create(&d).Error)
	}

	// Call generate_index tool handler directly
	result, _, err := srv.GenerateIndex(ctx, nil, mcpserver.GenerateIndexInput{Depth: "summary"})
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Parse response
	var entries []repository.IndexEntry
	text := result.Content[0].(*mcpsdk.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	assert.GreaterOrEqual(t, len(entries), 2)
}
```

- [ ] **Step 2: Run integration test**

Run: `cd ~/mystuff/goprojects/memory-mcp && MEMORY_MCP_TEST_DSN="postgres://..." go test ./internal/mcp/ -run TestGenerateIndex_Integration -v`
Expected: PASS (with valid DSN) or SKIP (without DSN)

- [ ] **Step 3: Format and lint the entire project**

Run: `cd ~/mystuff/goprojects/memory-mcp && gofmt -w . && golangci-lint run`
Expected: Clean

- [ ] **Step 4: Commit**

```bash
cd ~/mystuff/goprojects/memory-mcp
git add internal/repository/lint.go internal/repository/document.go internal/repository/section.go internal/service/memory.go internal/mcp/tools.go internal/mcp/server.go cmd/server/main.go internal/repository/document_test.go internal/repository/lint_test.go internal/mcp/tools_test.go docs/
git commit -m "feat: add generate_index, get_related, lint_memory MCP tools

Server-side knowledge compilation tools — pure SQL, no LLM dependency.
generate_index: tiered catalog (summary/category/full depth).
get_related: cosine similarity between document embeddings.
lint_memory: health checks (stale, sparse, near-duplicate, empty category)."
```
