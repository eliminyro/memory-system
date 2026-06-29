package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	apperr "github.com/eliminyro/memory-system/internal/errors"
)

// getIndex must call the service with the tenant from context and return JSON.
// We exercise the handler directly with an injected tenant context (the bearer
// middleware that populates it is authlet's, tested upstream).
func TestAPIGetIndex_RequiresServiceCall(t *testing.T) {
	h := &apiHandler{memory: nil} // nil memory => calling it panics; we assert wiring via routing instead
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/index?depth=summary", nil)
	req = req.WithContext(auth.WithTenantID(context.Background(), uuid.New()))

	defer func() {
		if recover() == nil {
			t.Fatal("expected getIndex to dereference h.memory (wiring check)")
		}
	}()
	h.mux().ServeHTTP(rec, req) // routes GET /index -> getIndex -> nil.GenerateIndex panics
	_, _ = json.Marshal(nil)
}

func TestAPIListDocuments_RequiresServiceCall(t *testing.T) {
	h := &apiHandler{memory: nil} // nil memory => calling it panics; we assert wiring via routing instead
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents?category=learnings", nil)
	req = req.WithContext(auth.WithTenantID(context.Background(), uuid.New()))

	defer func() {
		if recover() == nil {
			t.Fatal("expected listDocuments to dereference h.memory (wiring check)")
		}
	}()
	h.mux().ServeHTTP(rec, req) // routes GET /documents -> listDocuments -> nil.ListDocuments panics
	_, _ = json.Marshal(nil)
}

func TestAPISearch_RequiresQuery(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/search", nil) // no q
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing q = %d, want 400", rec.Code)
	}
}

func TestAPIGetDocument_InvalidID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents/not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", rec.Code)
	}
}

func TestWriteErr_Mapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{apperr.ErrNotFound, http.StatusNotFound},
		{apperr.ErrInvalidInput, http.StatusBadRequest},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeErr(rec, c.err)
		if rec.Code != c.want {
			t.Errorf("writeErr(%v) = %d, want %d", c.err, rec.Code, c.want)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf("writeErr body not JSON {error}: %s", rec.Body.String())
		}
	}
}

func TestUIConfigServed(t *testing.T) {
	_, cfg := uiHandlers("test-client-123")
	rec := httptest.NewRecorder()
	cfg(rec, httptest.NewRequest(http.MethodGet, "/ui/config.json", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("config not JSON: %v", err)
	}
	if body["client_id"] != "test-client-123" {
		t.Errorf("client_id = %q", body["client_id"])
	}
}

func TestUIRedirectPreservesQuery(t *testing.T) {
	rec := httptest.NewRecorder()
	uiRedirectHandler(rec, httptest.NewRequest(http.MethodGet, "/ui?code=abc&state=xyz", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/?code=abc&state=xyz" {
		t.Errorf("Location = %q, want /ui/?code=abc&state=xyz", loc)
	}
}

func TestUIRedirectNoQuery(t *testing.T) {
	rec := httptest.NewRecorder()
	uiRedirectHandler(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("Location = %q, want /ui/", loc)
	}
}

func TestAPIPatchSection_InvalidID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/sections/not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAPIVerifySection_InvalidID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sections/xxx/verify", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAPIDeleteDocument_InvalidID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/documents/xxx", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}
