//go:build integration

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eliminyro/memory-system/internal/service"
)

// TestAPISearchBounds proves getSearch enforces the same input bounds MCP
// search_memory does, via the shared service consts (L11): an over-long q is a
// 400, while an absurd ?limit is clamped to MaxSearchLimit rather than erroring.
func TestAPISearchBounds(t *testing.T) {
	f := newAPIFixture(t)

	t.Run("over-long q -> 400", func(t *testing.T) {
		long := strings.Repeat("a", service.MaxQueryLen+1)
		rec := httptest.NewRecorder()
		f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/search?q="+long, f.tenant))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("absurd limit clamped -> 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		f.h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/search?q=ok&limit=100000000", f.tenant))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
	})
}
