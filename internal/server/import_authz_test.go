package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// newImportAuthzSvc builds a MemoryService wired only with an in-memory authz
// store — no database at all. IsAdmin/CanManageTenant never touch the
// database (they resolve purely through authz.Check), so this is enough to
// drive the relational import surface's authorization decision (design.md
// §8) in isolation. Mirrors newACLNoDBSvc in internal/service/acl_test.go.
func newImportAuthzSvc(store authz.Store) *service.MemoryService {
	return service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
}

// TestRelationalImportEnqueue_SysAdminAnyTenant proves a system admin may
// enqueue an import into any tenant, including one they hold no relation on
// at all (design.md §8).
func TestRelationalImportEnqueue_SysAdminAnyTenant(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	fake := &fakeImportJobs{}
	h := &apiHandler{memory: svc, importJobs: fake, maxUploadBytes: 1 << 20}
	target := uuid.New()

	body, ct := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload"), target.String())
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithLocalAdmin(req.Context()))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if fake.created == nil || fake.created.TenantID != target {
		t.Fatalf("expected a job created for tenant %s, got %+v", target, fake.created)
	}
}

// TestRelationalImportEnqueue_ManagerOfTarget proves a tenant#manager (not a
// system admin) may enqueue into the tenant they manage.
func TestRelationalImportEnqueue_ManagerOfTarget(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	fake := &fakeImportJobs{}
	h := &apiHandler{memory: svc, importJobs: fake, maxUploadBytes: 1 << 20}

	target := uuid.New()
	subj := "mgr-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.TenantManager(target, subj)); err != nil {
		t.Fatalf("seed manager tuple: %v", err)
	}

	body, ct := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload"), target.String())
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithSubject(req.Context(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if fake.created == nil || fake.created.TenantID != target {
		t.Fatalf("expected a job created for tenant %s, got %+v", target, fake.created)
	}
}

// TestRelationalImportEnqueue_NonManagerForbidden proves an ordinary member of
// the target tenant (not a manager) is refused: 403, no job created.
func TestRelationalImportEnqueue_NonManagerForbidden(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	fake := &fakeImportJobs{}
	h := &apiHandler{memory: svc, importJobs: fake, maxUploadBytes: 1 << 20}

	target := uuid.New()
	subj := "mem-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.TenantMember(target, subj)); err != nil {
		t.Fatalf("seed member tuple: %v", err)
	}

	body, ct := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload"), target.String())
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithSubject(req.Context(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}))
	rec := httptest.NewRecorder()

	h.enqueueImport(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if fake.created != nil {
		t.Error("no job should be created for a non-manager caller")
	}
}

// TestRelationalImportStatus_SysAdminAnyTenant / _ManagerOfTarget /
// _NonManagerForbidden mirror the enqueue cases for GET /api/import/{id}: the
// same CanManageTenant/IsAdmin gate governs read access to a tenant's import
// jobs (design.md §8).
func TestRelationalImportStatus_SysAdminAnyTenant(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	target := uuid.New()
	jobID := uuid.New()
	fake := &fakeImportJobs{getJob: &models.ImportJob{ID: jobID, TenantID: target, Status: models.ImportJobStatusRunning}}
	h := &apiHandler{memory: svc, importJobs: fake}

	req := httptest.NewRequest(http.MethodGet, "/import/"+jobID.String()+"?tenant_id="+target.String(), nil)
	req.SetPathValue("id", jobID.String())
	req = req.WithContext(auth.WithLocalAdmin(req.Context()))
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelationalImportStatus_ManagerOfTarget(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	target := uuid.New()
	jobID := uuid.New()
	subj := "mgr-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.TenantManager(target, subj)); err != nil {
		t.Fatalf("seed manager tuple: %v", err)
	}
	fake := &fakeImportJobs{getJob: &models.ImportJob{ID: jobID, TenantID: target, Status: models.ImportJobStatusRunning}}
	h := &apiHandler{memory: svc, importJobs: fake}

	req := httptest.NewRequest(http.MethodGet, "/import/"+jobID.String()+"?tenant_id="+target.String(), nil)
	req.SetPathValue("id", jobID.String())
	req = req.WithContext(auth.WithSubject(req.Context(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}))
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelationalImportStatus_NonManagerForbidden(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	target := uuid.New()
	jobID := uuid.New()
	subj := "mem-" + uuid.NewString()
	if err := store.Write(context.Background(), authzseed.TenantMember(target, subj)); err != nil {
		t.Fatalf("seed member tuple: %v", err)
	}
	fake := &fakeImportJobs{getJob: &models.ImportJob{ID: jobID, TenantID: target, Status: models.ImportJobStatusRunning}}
	h := &apiHandler{memory: svc, importJobs: fake}

	req := httptest.NewRequest(http.MethodGet, "/import/"+jobID.String()+"?tenant_id="+target.String(), nil)
	req.SetPathValue("id", jobID.String())
	req = req.WithContext(auth.WithSubject(req.Context(), auth.Subject{Type: auth.SubjectTypeUser, ID: subj}))
	rec := httptest.NewRecorder()

	h.importStatus(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAPIMux_ImportRoutesWired proves both the relational /api/import surface
// and the pre-existing /api/admin/import surface are reachable through the
// same apiHandler.mux() (design.md §8: both coexist during the frontend
// transition — the already-deployed import page (#58) still calls
// /admin/import until its own follow-up PR switches to /import).
func TestAPIMux_ImportRoutesWired(t *testing.T) {
	store := authz.NewMemoryStore()
	svc := newImportAuthzSvc(store)
	h := &apiHandler{memory: svc, importJobs: &fakeImportJobs{}, maxUploadBytes: 1 << 20}
	mux := h.mux()

	target := uuid.New()
	body, ct := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload"), target.String())
	req := httptest.NewRequest(http.MethodPost, "/import", body)
	req.Header.Set("Content-Type", ct)
	req = req.WithContext(auth.WithLocalAdmin(req.Context()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /import via mux: status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	adminTarget := uuid.New()
	body2, ct2 := multipartArchiveWithTenant(t, []byte("PK\x03\x04 payload2"), adminTarget.String())
	req2 := httptest.NewRequest(http.MethodPost, "/admin/import", body2)
	req2.Header.Set("Content-Type", ct2)
	req2 = req2.WithContext(auth.WithLocalAdmin(req2.Context()))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("POST /admin/import via mux: status = %d, want 202; body=%s", rec2.Code, rec2.Body.String())
	}
}
