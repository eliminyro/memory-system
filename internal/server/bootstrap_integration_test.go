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
		nil, nil, nil, nil,
		store,
	)
	svc.BootstrapToken = token
	return NewHandler(Deps{DB: db, Memory: svc})
}

// TestBootstrapEndpoint_ProvisionsThenRejects drives the full stack (design D1
// bootstrapped-state derivation, D2 one-shot, D3 routing, D4 fail-closed token)
// end to end: /ui serves the setup view pre-bootstrap, POST /api/bootstrap
// provisions and returns the plaintext key once, /ui then serves the normal
// shell redirect, and a second bootstrap attempt is rejected as already-done.
func TestBootstrapEndpoint_ProvisionsThenRejects(t *testing.T) {
	token := "s3cr3t-" + uuid.NewString()
	h := newBootstrapHandler(t, token)

	// Pre-bootstrap: /ui serves the setup view.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui pre-bootstrap status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bootstrap") {
		t.Fatalf("/ui pre-bootstrap body missing setup view marker: %s", rec.Body.String())
	}

	// A bad token is rejected and provisions nothing.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(`{"token":"wrong"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad-token bootstrap status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	// The right token provisions and returns the plaintext key exactly once.
	rec = httptest.NewRecorder()
	body := `{"token":"` + token + `","tenant_name":"admin-` + uuid.NewString() + `"}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(body)))
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

	// Post-bootstrap: /ui redirects to the normal shell instead of setup.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("/ui post-bootstrap status = %d, want 302 (%s)", rec.Code, rec.Body.String())
	}

	// A second bootstrap attempt (even with the valid token) is rejected.
	rec = httptest.NewRecorder()
	body = `{"token":"` + token + `"}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second bootstrap status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}
