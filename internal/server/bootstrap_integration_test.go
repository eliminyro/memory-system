//go:build integration

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// clearAdmins removes every system:memory#admin tuple so a test starts from an
// un-bootstrapped instance. system:memory is a global singleton object (not
// tenant-scoped), so unlike other fixtures it cannot be isolated by a random
// UUID; the integration suite runs with -p 1, so this global reset is safe.
// Mirrors internal/service/bootstrap_integration_test.go's clearAdmins.
func clearAdmins(t *testing.T, db *gorm.DB) {
	t.Helper()
	del := func() {
		db.Exec("DELETE FROM relation_tuples WHERE object_type = ? AND object_id = ? AND relation = ?",
			authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
	}
	del()
	t.Cleanup(del)
}

// newBootstrapHandler builds the real NewHandler wiring (no AuthletWiring —
// bootstrap and the UI gate need no JWT infra) over a real Postgres-backed
// MemoryService, starting from an un-bootstrapped state.
func newBootstrapHandler(t *testing.T, token string) http.Handler {
	t.Helper()
	db := openAPIPG(t)
	clearAdmins(t, db)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db, nil, nil, nil,
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil, nil,
		store,
	)
	svc.BootstrapToken = token
	return NewHandler(Deps{DB: db, Memory: svc})
}

// jsonReq builds a POST whose body the /bootstrap handler decodes as JSON.
// parseBootstrapRequest branches on Content-Type; without this header it would
// form-parse the JSON body and read an empty token (→ a spurious 403).
func jsonReq(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestBootstrapEndpoint_ProvisionsThenRejects drives the full stack (design D1
// bootstrapped-state derivation, D2 one-shot, D3 /bootstrap routing, D4
// fail-closed token, D5 /ui gating) end to end: /bootstrap serves the page and
// /ui 404s pre-bootstrap (no AuthletWiring, so OAuth is not configured
// either), POST /bootstrap provisions and returns the plaintext key once,
// /bootstrap then 404s and /ui serves the no-oauth page, and a second
// bootstrap attempt is rejected as already-done.
func TestBootstrapEndpoint_ProvisionsThenRejects(t *testing.T) {
	token := "s3cr3t-" + uuid.NewString()
	h := newBootstrapHandler(t, token)

	// Pre-bootstrap: /bootstrap serves the page, /ui 404s.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/bootstrap pre-bootstrap status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bootstrap-form") {
		t.Fatalf("/bootstrap pre-bootstrap body missing form marker: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/ui pre-bootstrap status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}

	// A bad token is rejected and provisions nothing.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq(http.MethodPost, "/bootstrap", `{"token":"wrong"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad-token bootstrap status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The right token provisions and returns the plaintext key exactly once.
	rec = httptest.NewRecorder()
	body := `{"token":"` + token + `"}`
	h.ServeHTTP(rec, jsonReq(http.MethodPost, "/bootstrap", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp bootstrapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp.APIKey == "" || resp.TenantID == "" || resp.KeyID == "" {
		t.Fatalf("incomplete bootstrap response: %+v", resp)
	}

	// Post-bootstrap: /bootstrap 404s, /ui serves the no-oauth page (no
	// AuthletWiring in this test's Deps).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/bootstrap post-bootstrap status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui post-bootstrap status = %d, want 200 (no-oauth page) (%s)", rec.Code, rec.Body.String())
	}

	// A second bootstrap attempt (even with the valid token) is rejected —
	// though the front guard now shadows it with 404 rather than reaching
	// service.Bootstrap's 409, since /bootstrap is one-shot at the HTTP layer
	// (design D3: "Bootstrap endpoint disabled after bootstrap").
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq(http.MethodPost, "/bootstrap", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second bootstrap status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}
