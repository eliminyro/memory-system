//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// TestAPIListDocuments_Pagination proves GET /api/documents parses limit/offset,
// applies the default page when absent/non-positive, clamps to MaxListLimit, and
// floors a negative offset — while the body stays a bare document array (D3).
func TestAPIListDocuments_Pagination(t *testing.T) {
	f := newAPIFixture(t)
	db := openAPIPG(t) // same TEST_DATABASE_URL — seeds rows the handler reads

	category := "bulk-" + uuid.NewString()[:8]
	const total = service.MaxListLimit + 5 // straddles the clamp
	docs := make([]models.Document, total)
	for i := range docs {
		docs[i] = models.Document{
			ID:       uuid.New(),
			TenantID: f.tenant,
			Category: category,
			Slug:     fmt.Sprintf("b%04d-%s", i, uuid.NewString()[:8]),
			Title:    fmt.Sprintf("Bulk %d", i),
		}
	}
	if err := db.CreateInBatches(&docs, 100).Error; err != nil {
		t.Fatalf("seed bulk docs: %v", err)
	}

	get := func(target string) []models.Document {
		t.Helper()
		rec := httptest.NewRecorder()
		f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, target, f.tenant))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var out []models.Document
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body not a documents array: %v (%s)", err, rec.Body.String())
		}
		return out
	}

	base := "/documents?category=" + category
	// Absent limit -> default page size.
	if n := len(get(base)); n != service.DefaultListLimit {
		t.Errorf("absent limit returned %d docs, want default %d", n, service.DefaultListLimit)
	}
	// Non-positive limit -> default (the HTTP path always paginates).
	if n := len(get(base + "&limit=0")); n != service.DefaultListLimit {
		t.Errorf("limit=0 returned %d docs, want default %d", n, service.DefaultListLimit)
	}
	// Over-max limit -> clamped to the maximum, not the unbounded request.
	if n := len(get(base + "&limit=100000")); n != service.MaxListLimit {
		t.Errorf("oversized limit returned %d docs, want clamp %d", n, service.MaxListLimit)
	}
	// Negative offset -> floored at 0 (returns the first page).
	if n := len(get(base + "&limit=5&offset=-3")); n != 5 {
		t.Errorf("negative offset returned %d docs, want 5 from offset 0", n)
	}
}
