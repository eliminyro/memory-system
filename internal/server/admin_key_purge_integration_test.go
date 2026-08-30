//go:build integration

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestAdminAPI_KeyPurge covers the hard-delete endpoint (DELETE /admin/keys/{id}/purge):
// an active key is refused (it must be revoked or expired first), then a revoked key
// purges with 204 and disappears from the listing. The soft revoke on
// DELETE /admin/keys/{id} is unaffected.
func TestAdminAPI_KeyPurge(t *testing.T) {
	h, adminCtx, _ := newAdminHarness(t)

	// Personal tenant + key (keys are personal-tenant only).
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants", map[string]string{"name": "purge-" + uuid.NewString(), "type": models.TenantTypePersonal, "owner_email": "owner-" + uuid.NewString() + "@example.com"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d (%s)", rec.Code, rec.Body.String())
	}
	var tenant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tenant); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants/"+tenant.ID+"/keys", map[string]string{"label": "agent"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue key = %d (%s)", rec.Code, rec.Body.String())
	}
	var issued struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}

	// Purge is refused while the key is still active.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/keys/"+issued.ID+"/purge", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("purge active key = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// Revoke, then purge -> 204.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/keys/"+issued.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke key = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/keys/"+issued.ID+"/purge", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("purge revoked key = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}

	// The purged key is gone from the listing.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodGet, "/admin/tenants/"+tenant.ID+"/keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), issued.ID) {
		t.Errorf("purged key still present in listing: %s", rec.Body.String())
	}
}
