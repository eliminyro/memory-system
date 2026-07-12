//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// newAdminHarness builds a real apiHandler (mux includes /admin + the adminOnly
// gate) plus an admin context (subject holding system:memory#admin) and a non-admin one.
func newAdminHarness(t *testing.T) (*apiHandler, context.Context, context.Context) {
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
		nil, nil, nil, nil,
		store,
	)
	adminSubj := "admin-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.SystemAdmin(adminSubj)); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	adminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: adminSubj})
	nonAdminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: "nobody-" + uuid.NewString()})
	return &apiHandler{memory: svc}, adminCtx, nonAdminCtx
}

func adminReq(t *testing.T, ctx context.Context, method, target string, body any) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r.WithContext(ctx)
}

func TestAdminAPI_GateRejectsNonAdmin(t *testing.T) {
	h, adminCtx, nonAdminCtx := newAdminHarness(t)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, nonAdminCtx, http.MethodGet, "/admin/whoami", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin whoami = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodGet, "/admin/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin whoami = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// A protected mutation is also refused for a non-admin.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, nonAdminCtx, http.MethodPost, "/admin/tenants", map[string]string{"name": "x"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin create tenant = %d, want 403", rec.Code)
	}
}

func TestAdminAPI_KeyLifecycleNeverLeaksSecret(t *testing.T) {
	h, adminCtx, _ := newAdminHarness(t)

	// Create a tenant.
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants", map[string]string{"name": "acme-" + uuid.NewString()}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d (%s)", rec.Code, rec.Body.String())
	}
	var tenant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tenant); err != nil {
		t.Fatal(err)
	}

	// Issue a key -> plaintext returned once.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants/"+tenant.ID+"/keys", map[string]string{"label": "agent"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue key = %d (%s)", rec.Code, rec.Body.String())
	}
	var issued struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Key, "mmcp_") {
		t.Fatalf("issued key not a plaintext token: %q", issued.Key)
	}

	// List keys -> metadata only: neither the plaintext nor the hash present.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodGet, "/admin/tenants/"+tenant.ID+"/keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys = %d", rec.Code)
	}
	listBody := rec.Body.String()
	if strings.Contains(listBody, issued.Key) {
		t.Error("key listing leaked the plaintext key")
	}
	if strings.Contains(listBody, "key_hash") || strings.Contains(listBody, "KeyHash") {
		t.Errorf("key listing exposed the hash field: %s", listBody)
	}

	// Rotate -> a new plaintext, different from the first.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/keys/"+issued.ID+"/rotate", map[string]int{"grace_hours": 1}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d (%s)", rec.Code, rec.Body.String())
	}
	var rotated struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Key == "" || rotated.Key == issued.Key {
		t.Fatalf("rotate did not return a distinct new key")
	}

	// Revoke the (already-rotated) key id -> 204.
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/keys/"+issued.ID, nil))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusNotFound {
		t.Fatalf("revoke = %d, want 204 or 404-already-revoked", rec.Code)
	}
}

func TestAdminAPI_UserLifecycle(t *testing.T) {
	h, adminCtx, _ := newAdminHarness(t)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/tenants", map[string]string{"name": "u-" + uuid.NewString()}))
	var tenant struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tenant)

	email := "member-" + uuid.NewString() + "@example.com"
	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPost, "/admin/users",
		map[string]string{"email": email, "tenant_id": tenant.ID, "role": "member"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant user = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodGet, "/admin/tenants/"+tenant.ID+"/users", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), email) {
		t.Fatalf("list users missing grant: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodPatch, "/admin/users",
		map[string]string{"email": email, "role": "admin"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("set role = %d (%s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.mux().ServeHTTP(rec, adminReq(t, adminCtx, http.MethodDelete, "/admin/users?email="+email, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke user = %d (%s)", rec.Code, rec.Body.String())
	}
}
