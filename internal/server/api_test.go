package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
)

// The behavioral counterparts to the former nil-panic "wiring check" tests
// (TestAPIGetIndex_RequiresServiceCall / TestAPIListDocuments_RequiresServiceCall)
// now live in api_integration_test.go: they inject a real MemoryService and
// assert each handler forwards the context tenant + query params and marshals
// the returned data.

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

// An unmapped (internal) error must not leak its string to the client; the
// body is a fixed generic message.
func TestWriteErr_UnmappedDoesNotLeak(t *testing.T) {
	secret := "pq: password authentication failed for user \"memory\""
	rec := httptest.NewRecorder()
	writeErr(rec, errors.New(secret))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["error"] != "internal error" {
		t.Errorf("error body = %q, want generic %q", body["error"], "internal error")
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "memory") {
		t.Errorf("internal error string leaked to client: %s", rec.Body.String())
	}
}

// Mapped sentinels still surface their (safe) message.
func TestWriteErr_MappedExposesMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, apperr.ErrNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("expected mapped message on the wire, got %s", rec.Body.String())
	}
}

func TestUIConfigServed(t *testing.T) {
	const base = "https://mem.example.org"
	_, cfg := uiHandlers("test-client-123", base)
	rec := httptest.NewRecorder()
	cfg(rec, httptest.NewRequest(http.MethodGet, "/ui/config.json", nil))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("config not JSON: %v", err)
	}
	if body["client_id"] != "test-client-123" {
		t.Errorf("client_id = %q", body["client_id"])
	}
	// OAuth config must be derived from the configured base URL, never
	// hardcoded — this is the deployment-portability guarantee.
	if body["issuer"] != base {
		t.Errorf("issuer = %q, want %q", body["issuer"], base)
	}
	if body["redirect_uri"] != base+"/ui" {
		t.Errorf("redirect_uri = %q, want %q", body["redirect_uri"], base+"/ui")
	}
	if body["resource"] != base+"/mcp" {
		t.Errorf("resource = %q, want %q", body["resource"], base+"/mcp")
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

func TestAPIPatchSection_EmptyBody(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/sections/"+uuid.NewString(), strings.NewReader(`{}`))
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestAPIPatchDocument_InvalidID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/documents/not-a-uuid", strings.NewReader(`{"title":"x"}`))
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}
