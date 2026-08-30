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

// TestAdminAPI_DeleteTenant covers DELETE /admin/tenants/{id}: non-admins are
// refused by the gate, an admin deletes a tenant (204) and it drops from the
// listing, and the bootstrap/default tenant is refused (400).
func TestAdminAPI_DeleteTenant(t *testing.T) {
	h, adminCtx, nonAdminCtx := newAdminHarness(t)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants", map[string]string{"name": "del-" + uuid.NewString()}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d (%s)", rec.Code, rec.Body.String())
	}
	var tenant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tenant); err != nil {
		t.Fatal(err)
	}

	// Non-admin is refused by the adminOnly gate.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, nonAdminCtx, http.MethodDelete, "/admin/tenants/"+tenant.ID, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete = %d, want 403", rec.Code)
	}

	// Admin deletes -> 204.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/tenants/"+tenant.ID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete tenant = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}

	// Gone from the listing.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodGet, "/admin/tenants", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list tenants = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), tenant.ID) {
		t.Errorf("deleted tenant still listed: %s", tenant.ID)
	}

	// The bootstrap/default tenant is protected.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/tenants/"+models.BootstrapTenantID.String(), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete bootstrap tenant = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}
