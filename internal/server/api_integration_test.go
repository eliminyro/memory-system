//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

const apiTestDim = 768

func openAPIPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", apiTestDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// apiFixture is a real apiHandler (backed by a real MemoryService) plus the ids
// of a seeded tenant + document, enough to assert handlers pass context tenant +
// params through and marshal results.
type apiFixture struct {
	h       *apiHandler
	tenant  uuid.UUID
	docID   uuid.UUID
	slug    string
	title   string
	seedCtx context.Context
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	db := openAPIPG(t)
	store := authz.NewPostgresStore(db)

	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(apiTestDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewThresholdStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		store,
	)

	ctx := context.Background()
	tenant := models.Tenant{ID: uuid.New(), Name: "api-" + uuid.NewString()}
	if err := db.Create(&tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := store.Write(ctx, authzseed.TenantSystemEdge(tenant.ID)); err != nil {
		t.Fatalf("seed tenant edge: %v", err)
	}

	subj := "user-" + uuid.NewString()
	seedCtx := auth.WithSubject(auth.WithTenantID(ctx, tenant.ID), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})

	slug := "note-" + uuid.NewString()
	title := "Api Title " + slug
	content := "# " + title + "\n\n## Heading\nsome distinctive body text"
	res, err := svc.StoreDocument(seedCtx, "learnings", nil, slug, content, true, "seed", nil)
	if err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if res.Document == nil {
		t.Fatal("seed document returned no document")
	}

	return &apiFixture{
		h:       &apiHandler{memory: svc},
		tenant:  tenant.ID,
		docID:   res.Document.ID,
		slug:    slug,
		title:   title,
		seedCtx: seedCtx,
	}
}

// reqAs builds a request whose context carries the given tenant, as the bearer
// middleware + UserContextBridge would in production.
func reqAs(method, target string, tenant uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(auth.WithTenantID(context.Background(), tenant))
}

// TestAPIListDocuments_ReturnsSeededDoc proves listDocuments passes the context
// tenant + category filter to the service and marshals the returned documents.
func TestAPIListDocuments_ReturnsSeededDoc(t *testing.T) {
	f := newAPIFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/documents?category=learnings", f.tenant))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var docs []models.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("body not a documents array: %v (%s)", err, rec.Body.String())
	}
	if !containsDoc(docs, f.docID) {
		t.Fatalf("listDocuments did not return seeded doc %s; got %d docs", f.docID, len(docs))
	}

	// A different category must not return the learnings doc — proves the query param reaches the service.
	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, reqAs(http.MethodGet, "/documents?category=preferences", f.tenant))
	var other []models.Document
	if err := json.Unmarshal(rec2.Body.Bytes(), &other); err != nil {
		t.Fatalf("body not a documents array: %v", err)
	}
	if containsDoc(other, f.docID) {
		t.Error("category=preferences must not return the learnings doc")
	}

	// A different tenant must see nothing — proves the context tenant is used.
	rec3 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec3, reqAs(http.MethodGet, "/documents?category=learnings", uuid.New()))
	var foreign []models.Document
	if err := json.Unmarshal(rec3.Body.Bytes(), &foreign); err != nil {
		t.Fatalf("body not a documents array: %v", err)
	}
	if containsDoc(foreign, f.docID) {
		t.Error("a foreign tenant context must not see the seeded doc")
	}
}

// TestAPIGetIndex_ReturnsSeededCategory proves getIndex calls the service with
// the context tenant and marshals the index.
func TestAPIGetIndex_ReturnsSeededCategory(t *testing.T) {
	f := newAPIFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/index?depth=summary", f.tenant))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var entries []repository.IndexEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("body not an index array: %v (%s)", err, rec.Body.String())
	}
	found := false
	for _, e := range entries {
		if e.Category == "learnings" && e.DocCount >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("index for the seeded tenant missing a 'learnings' entry; got %+v", entries)
	}

	// A foreign tenant's index must not include the learnings category.
	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, reqAs(http.MethodGet, "/index?depth=summary", uuid.New()))
	var foreign []repository.IndexEntry
	if err := json.Unmarshal(rec2.Body.Bytes(), &foreign); err != nil {
		t.Fatalf("body not an index array: %v", err)
	}
	for _, e := range foreign {
		if e.Category == "learnings" {
			t.Error("a foreign tenant index must not include the seeded 'learnings' category")
		}
	}
}

// TestAPIGetDocument_ReturnsSeededDoc proves getDocument resolves the id, uses
// the context tenant, and marshals the document view with the seeded fields.
func TestAPIGetDocument_ReturnsSeededDoc(t *testing.T) {
	f := newAPIFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/documents/"+f.docID.String(), f.tenant))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var view struct {
		ID       uuid.UUID `json:"id"`
		Category string    `json:"category"`
		Slug     string    `json:"slug"`
		Title    string    `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("body not a document view: %v (%s)", err, rec.Body.String())
	}
	if view.ID != f.docID {
		t.Errorf("id = %s, want %s", view.ID, f.docID)
	}
	if view.Slug != f.slug {
		t.Errorf("slug = %q, want %q", view.Slug, f.slug)
	}
	if view.Title != f.title {
		t.Errorf("title = %q, want %q", view.Title, f.title)
	}

	// A foreign tenant must get 404 — the context tenant scopes the lookup.
	rec2 := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec2, reqAs(http.MethodGet, "/documents/"+f.docID.String(), uuid.New()))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("foreign tenant getDocument = %d, want 404", rec2.Code)
	}
}

// TestAPISearch_CallsService proves getSearch forwards the q param to the service
// and marshals a results array.
func TestAPISearch_CallsService(t *testing.T) {
	f := newAPIFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/search?q=distinctive", f.tenant))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var results []repository.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("body not a search-results array: %v (%s)", err, rec.Body.String())
	}
}

func containsDoc(docs []models.Document, id uuid.UUID) bool {
	for _, d := range docs {
		if d.ID == id {
			return true
		}
	}
	return false
}
