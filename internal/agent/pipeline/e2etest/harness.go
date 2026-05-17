// Package e2etest provides an in-process integration harness for the
// memory-system agent end-to-end suite. It boots the real MCP server
// (HTTP + auth + service + repositories + migrations) against an isolated
// Postgres schema, swaps the embedding provider for a deterministic
// FakeEmbedder, and exposes seed/count helpers for tests.
//
// This file (Task 4) wires the harness skeleton. Seed/count helpers land
// in Task 5; the FakeLLM body fills out in Task 6; the 12 e2e tests use
// the harness in Tasks 8-10.
package e2etest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/eliminyro/memory-system/internal/agent/mcpclient"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/mcp"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/server"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// embeddingDim is the vector width used throughout the e2e suite. It is
// independent of the production default — FakeEmbedder produces vectors of
// whatever size we ask for, and a smaller dim keeps test fixtures cheap.
const embeddingDim = 768

// Harness wires a fully-running memory-system server in-process against a
// fresh Postgres schema, with a seeded tenant + API key.
//
// Task 4 builds the server stack and the schema-isolated DB. Tenant, APIKey,
// Token, and MCPClient stay zero-valued until Task 5 adds seedTenant().
// LLM is a placeholder until Task 6.
type Harness struct {
	T         testing.TB
	DB        *gorm.DB
	Server    *httptest.Server
	MCPClient *mcpclient.Client
	Tenant    models.Tenant
	APIKey    models.APIKey
	Token     string
	LLM       *FakeLLM
	Model     string
}

// New creates a Harness backed by a fresh per-test Postgres schema. The
// schema is dropped on t.Cleanup. The test is skipped when TEST_DATABASE_URL
// is unset so the default `go test ./...` run (no Postgres) stays green.
//
// The dep graph mirrors cmd/server/main.go exactly, with these production
// pieces stubbed:
//   - service.NewFakeEmbedder in place of the GCP/Ollama provider
//   - AuthletWiring: nil (API-key path only)
//   - no cleanup scanner (tests don't need the nightly job)
//   - no Telegram notifier
func New(t testing.TB) *Harness {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	schema := "e2e_" + randHex(8)
	db := openWithSchema(t, dbURL, schema)
	t.Cleanup(func() { dropSchema(t, dbURL, schema) })

	if err := database.Migrate(db, embeddingDim, database.TenantColumnDefaults{
		StalenessMode:      "off",
		DuplicateGuard:     false,
		CleanupScanEnabled: false,
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Repositories — same constructors and order as cmd/server/main.go.
	docRepo := repository.NewDocumentRepository(db)
	sectionRepo := repository.NewSectionRepository(db)
	tenantRepo := repository.NewTenantRepository(db)
	keyRepo := repository.NewAPIKeyRepository(db)
	lintRepo := repository.NewLintRepository(db)
	overrideRepo := repository.NewOverrideLogRepository(db)
	cleanupRepo := repository.NewCleanupQueueRepository(db)

	// Staleness threshold cache — same as production.
	thresholdStore := staleness.NewThresholdStore(db)

	// Embedder: deterministic FakeEmbedder so tests don't hit external APIs
	// and assertions on vector content are stable.
	embedder := service.NewFakeEmbedder(embeddingDim)

	// No admin emails — tests exercise non-admin paths. Admin-specific tests
	// can override via a future helper.
	var allowedEmails []string

	memorySvc := service.NewMemoryService(
		db, docRepo, sectionRepo, embedder,
		tenantRepo, keyRepo, lintRepo, thresholdStore, overrideRepo, cleanupRepo,
		allowedEmails,
	)

	// Cleanup scanner intentionally skipped — see package doc.

	mcpServer := mcp.NewServer(memorySvc, allowedEmails)
	keyValidator := auth.NewAPIKeyValidator(db)

	handler := server.NewHandler(server.Deps{
		DB:            db,
		MCPServer:     mcpServer,
		KeyValidator:  keyValidator,
		AuthletWiring: nil, // API-key-only path
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tenant, ak, token := seedTenant(t, db)

	mcpURL, err := url.JoinPath(srv.URL, "mcp")
	if err != nil {
		t.Fatalf("compose mcp url: %v", err)
	}
	mc := mcpclient.New(mcpURL, token)

	return &Harness{
		T:         t,
		DB:        db,
		Server:    srv,
		MCPClient: mc,
		Tenant:    tenant,
		APIKey:    ak,
		Token:     token,
		LLM:       NewFakeLLM(),
		Model:     "fake-model",
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// openWithSchema creates a fresh Postgres schema and returns a *gorm.DB
// scoped to it via search_path. The admin connection used to create the
// schema is closed before returning — only the scoped handle survives.
func openWithSchema(t testing.TB, dbURL, schema string) *gorm.DB {
	t.Helper()
	admin, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schema)).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if sqlDB, err := admin.DB(); err == nil {
		_ = sqlDB.Close()
	}

	scopedURL := appendSearchPath(t, dbURL, schema)
	db, err := gorm.Open(postgres.Open(scopedURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open scoped db: %v", err)
	}
	// pgvector lives in the schema where it's first created — enable here
	// so the migration's vector columns resolve inside our isolated schema.
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		t.Fatalf("enable pgvector: %v", err)
	}
	return db
}

func appendSearchPath(t testing.TB, dbURL, schema string) string {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatalf("parse db url: %v", err)
	}
	q := u.Query()
	// Include public in the search_path so the pgvector extension's `vector`
	// type (installed in public by the pgvector image) is resolvable from
	// the per-test schema during migrations and queries.
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

func dropSchema(t testing.TB, dbURL, schema string) {
	t.Helper()
	admin, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Logf("drop schema: open admin db: %v", err)
		return
	}
	if err := admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)).Error; err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
	if sqlDB, err := admin.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// seedTenant creates one tenant, one issued API key, and the corresponding
// tenant_users row. It returns the tenant, the api_key row, and the plaintext
// token a test should pass to mcpclient. Each per-test schema is isolated, so
// the fixed email/name values don't collide across parallel runs.
func seedTenant(t testing.TB, db *gorm.DB) (models.Tenant, models.APIKey, string) {
	t.Helper()

	tenant := models.Tenant{
		Name:  "e2e-tenant",
		Email: "e2e@example.test",
	}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	ak := models.APIKey{
		TenantID: tenant.ID,
		KeyHash:  hash,
		Label:    "e2e-key",
		Prefix:   auth.KeyPrefix(plaintext),
	}
	if err := db.Create(&ak).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	tu := models.TenantUser{
		Email:    tenant.Email,
		TenantID: tenant.ID,
		Role:     models.TenantUserRoleAdmin,
	}
	if err := db.Create(&tu).Error; err != nil {
		t.Fatalf("seed tenant_user: %v", err)
	}

	return tenant, ak, plaintext
}

// SeedDocument inserts a document row directly via GORM. Used to set up
// pre-existing knowledge for merge-verdict scenarios. subcategory may be
// empty — it's stored as NULL in that case to match the *string column.
func (h *Harness) SeedDocument(category, subcategory, slug, title string) models.Document {
	h.T.Helper()
	var subPtr *string
	if subcategory != "" {
		subPtr = &subcategory
	}
	doc := models.Document{
		TenantID:    h.Tenant.ID,
		Category:    category,
		Subcategory: subPtr,
		Slug:        slug,
		Title:       title,
	}
	if err := h.DB.Create(&doc).Error; err != nil {
		h.T.Fatalf("seed document: %v", err)
	}
	return doc
}

// SeedSection inserts a section row directly via GORM with a deterministic
// FakeEmbedder vector. Used by merge-verdict scenarios to provide a known
// merge target by section_id. heading may be empty — stored as NULL.
func (h *Harness) SeedSection(doc models.Document, heading, content string) models.Section {
	h.T.Helper()
	embedder := service.NewFakeEmbedder(embeddingDim)
	vec, err := embedder.Embed(nil, content)
	if err != nil {
		h.T.Fatalf("embed seed section: %v", err)
	}
	var headPtr *string
	if heading != "" {
		headPtr = &heading
	}
	sec := models.Section{
		DocumentID: doc.ID,
		Heading:    headPtr,
		Content:    content,
		Embedding:  vec,
	}
	if err := h.DB.Create(&sec).Error; err != nil {
		h.T.Fatalf("seed section: %v", err)
	}
	return sec
}

// CountDocuments returns the row count of the documents table inside the
// harness's isolated schema. Used to assert "did the agent actually persist
// new knowledge".
func (h *Harness) CountDocuments() int64 {
	h.T.Helper()
	var n int64
	if err := h.DB.Model(&models.Document{}).Count(&n).Error; err != nil {
		h.T.Fatalf("count documents: %v", err)
	}
	return n
}

// CountSections returns the row count of the sections table.
func (h *Harness) CountSections() int64 {
	h.T.Helper()
	var n int64
	if err := h.DB.Model(&models.Section{}).Count(&n).Error; err != nil {
		h.T.Fatalf("count sections: %v", err)
	}
	return n
}

// GetSection fetches a section by id. Returns the raw gorm error so callers
// can branch on ErrRecordNotFound when asserting deletion or merge semantics.
func (h *Harness) GetSection(id any) (models.Section, error) {
	var s models.Section
	err := h.DB.First(&s, "id = ?", id).Error
	return s, err
}

// RevokeAPIKey marks the harness's seeded API key as revoked. Used by the
// MCPStoreError scenario to force a 401 on subsequent mcpclient calls.
func (h *Harness) RevokeAPIKey() {
	h.T.Helper()
	now := time.Now()
	if err := h.DB.Model(&h.APIKey).Update("revoked_at", &now).Error; err != nil {
		h.T.Fatalf("revoke api key: %v", err)
	}
}
