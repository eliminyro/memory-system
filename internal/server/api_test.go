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
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// Behavioral counterparts (handlers forwarding context tenant + params, marshaling
// results) live in api_integration_test.go with a real MemoryService.

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

// An unmapped (internal) error must not leak its string; the body is generic.
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
	// OAuth config derived from base URL, never hardcoded — the portability guarantee.
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

// jsonList must coerce a nil slice (empty corpus) to a JSON array, never null —
// a null body breaks the /ui client (design D2). The getIndex element type stands
// in for every list endpoint's empty-result path.
func TestJSONList_NilCoercesToArray(t *testing.T) {
	var empty []repository.IndexEntry // nil, as GenerateIndex returns on an empty corpus
	b, err := json.Marshal(jsonList(empty))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Fatalf("nil list marshaled to %s, want []", b)
	}
	// A populated slice passes through unchanged.
	if got := jsonList([]int{1, 2}); len(got) != 2 {
		t.Errorf("populated slice len = %d, want 2", len(got))
	}
}

// tenantFilter is the parse-and-produce-overrideID logic the read handlers feed
// straight into the service call, so testing it directly covers the ?tenant_id
// pass-through: absent/empty ⇒ nil (aggregate), valid ⇒ &id, malformed ⇒ error.
func TestTenantFilter(t *testing.T) {
	// absent ⇒ nil, no error
	if id, err := tenantFilter(httptest.NewRequest(http.MethodGet, "/search?q=x", nil)); err != nil || id != nil {
		t.Fatalf("absent tenant_id: id=%v err=%v, want nil,nil", id, err)
	}
	// present-but-empty ⇒ nil, no error
	if id, err := tenantFilter(httptest.NewRequest(http.MethodGet, "/search?q=x&tenant_id=", nil)); err != nil || id != nil {
		t.Fatalf("empty tenant_id: id=%v err=%v, want nil,nil", id, err)
	}
	// valid ⇒ &id equal to the parsed UUID, no error
	want := uuid.New()
	id, err := tenantFilter(httptest.NewRequest(http.MethodGet, "/search?q=x&tenant_id="+want.String(), nil))
	if err != nil {
		t.Fatalf("valid tenant_id: unexpected err %v", err)
	}
	if id == nil || *id != want {
		t.Fatalf("valid tenant_id: got %v, want &%v", id, want)
	}
	// malformed ⇒ error (handler turns this into 400; never a silent aggregate)
	if _, err := tenantFilter(httptest.NewRequest(http.MethodGet, "/search?q=x&tenant_id=not-a-uuid", nil)); err == nil {
		t.Fatal("malformed tenant_id: want error, got nil")
	}
}

// A malformed ?tenant_id must 400 before the handler ever reaches the service —
// so these run with a nil memory (parse failure short-circuits).
func TestAPISearch_MalformedTenantID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/search?q=x&tenant_id=not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed tenant_id = %d, want 400", rec.Code)
	}
}

func TestAPIListDocuments_MalformedTenantID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/documents?tenant_id=not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed tenant_id = %d, want 400", rec.Code)
	}
}

func TestAPIGetIndex_MalformedTenantID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/index?tenant_id=not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed tenant_id = %d, want 400", rec.Code)
	}
}

func TestAPIGetDocument_MalformedTenantID(t *testing.T) {
	h := &apiHandler{}
	rec := httptest.NewRecorder()
	// Valid path id so parsing gets past the id check and reaches the tenant_id parse.
	req := httptest.NewRequest(http.MethodGet, "/documents/"+uuid.NewString()+"?tenant_id=not-a-uuid", nil)
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed tenant_id = %d, want 400", rec.Code)
	}
}

// The search response marshals repository.SearchResult directly (writeJSON →
// json.Encode), so the owning-tenant labels must appear on the wire via the json tags.
func TestSearchResult_TenantLabelsInResponse(t *testing.T) {
	tid := uuid.New()
	res := []repository.SearchResult{{TenantID: tid, TenantName: "acme", TenantType: "shared"}}
	b, err := json.Marshal(jsonList(res))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0]["tenant_id"] != tid.String() {
		t.Errorf("tenant_id = %v, want %s", got[0]["tenant_id"], tid)
	}
	if got[0]["tenant_name"] != "acme" {
		t.Errorf("tenant_name = %v, want acme", got[0]["tenant_name"])
	}
	if got[0]["tenant_type"] != "shared" {
		t.Errorf("tenant_type = %v, want shared", got[0]["tenant_type"])
	}
}

// The get-document response marshals service.DocumentView directly, so its
// owning-tenant labels must appear on the wire via the json tags.
func TestDocumentView_TenantLabelsInResponse(t *testing.T) {
	tid := uuid.New()
	b, err := json.Marshal(service.DocumentView{TenantID: tid, TenantName: "acme", TenantType: "shared"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["tenant_id"] != tid.String() {
		t.Errorf("tenant_id = %v, want %s", got["tenant_id"], tid)
	}
	if got["tenant_name"] != "acme" {
		t.Errorf("tenant_name = %v, want acme", got["tenant_name"])
	}
	if got["tenant_type"] != "shared" {
		t.Errorf("tenant_type = %v, want shared", got["tenant_type"])
	}
}
