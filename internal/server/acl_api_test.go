package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/service"
)

// fakeACLService stubs aclService for handler unit tests: each method returns
// whatever error is configured, so tests can drive every writeACLErr branch
// (404 / 403 / 500) without a database. Mirrors fakeImportJobs in
// import_test.go.
type fakeACLService struct {
	err error // returned by every Grant*/Revoke* call

	tenantGrants   []service.Grant
	tenantGrantErr error
	docGrants      []service.Grant
	docGrantErr    error

	// lastRelation records the relation the handler actually passed through,
	// so a ceiling test can prove the handler didn't silently downgrade it.
	lastRelation string
}

func (f *fakeACLService) GrantTenantAccess(_ context.Context, _ uuid.UUID, _, relation string) error {
	f.lastRelation = relation
	return f.err
}

func (f *fakeACLService) RevokeTenantAccess(_ context.Context, _ uuid.UUID, _, relation string) error {
	f.lastRelation = relation
	return f.err
}

func (f *fakeACLService) ListTenantGrants(context.Context, uuid.UUID) ([]service.Grant, error) {
	return f.tenantGrants, f.tenantGrantErr
}

func (f *fakeACLService) GrantDocumentAccess(_ context.Context, _ uuid.UUID, _, relation string) error {
	f.lastRelation = relation
	return f.err
}

func (f *fakeACLService) RevokeDocumentAccess(_ context.Context, _ uuid.UUID, _, relation string) error {
	f.lastRelation = relation
	return f.err
}

func (f *fakeACLService) ListDocumentGrants(context.Context, uuid.UUID) ([]service.Grant, error) {
	return f.docGrants, f.docGrantErr
}

func grantReq(method, path, body string) *http.Request {
	return httptest.NewRequest(method, path, strings.NewReader(body))
}

// TestACLGrantTenant_BadRelation proves a relation outside {viewer, member,
// manager} is rejected 400 before ever reaching the service (design.md §7).
func TestACLGrantTenant_BadRelation(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("should not be called")}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodPost, "/tenants/"+tid.String()+"/grants", `{"email":"a@example.com","relation":"owner"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if fake.lastRelation != "" {
		t.Error("service must not be invoked for a bad relation")
	}
}

// TestACLGrantTenant_NotAuthorized proves a non-manager caller (service denies
// via the grant-ceiling matrix, ErrInvalidInput) surfaces as 403, not the
// service's own 400-shaped sentinel.
func TestACLGrantTenant_NotAuthorized(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("%w: not authorized to grant viewer on tenant", apperr.ErrInvalidInput)}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodPost, "/tenants/"+tid.String()+"/grants", `{"email":"a@example.com","relation":"viewer"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLGrantTenant_UnknownEmail proves an unresolvable email (service
// ErrNotFound) surfaces as 404.
func TestACLGrantTenant_UnknownEmail(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("%w: no user mapping for ghost@example.com", apperr.ErrNotFound)}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodPost, "/tenants/"+tid.String()+"/grants", `{"email":"ghost@example.com","relation":"member"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLGrantTenant_ManagerCannotAppointManager proves the ceiling case
// explicitly: a manager attempting to grant "manager" is a valid relation
// value (so it reaches the service) but the service denies it (ErrInvalidInput),
// and that must map to 403 (not 400) — the relation itself was well-formed.
func TestACLGrantTenant_ManagerCannotAppointManager(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("%w: not authorized to grant manager on tenant", apperr.ErrInvalidInput)}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodPost, "/tenants/"+tid.String()+"/grants", `{"email":"a@example.com","relation":"manager"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ceiling denial, not a bad-relation 400); body=%s", rec.Code, rec.Body.String())
	}
	if fake.lastRelation != "manager" {
		t.Errorf("service should have been invoked with relation=manager, got %q", fake.lastRelation)
	}
}

// TestACLGrantTenant_Success proves the happy path: 201 with the echoed grant.
func TestACLGrantTenant_Success(t *testing.T) {
	fake := &fakeACLService{}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodPost, "/tenants/"+tid.String()+"/grants", `{"email":"a@example.com","relation":"viewer"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLRevokeTenant_Success proves revoke is a 204 with no body.
func TestACLRevokeTenant_Success(t *testing.T) {
	fake := &fakeACLService{}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := grantReq(http.MethodDelete, "/tenants/"+tid.String()+"/grants", `{"email":"a@example.com","relation":"member"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLGrantDocument_BadRelation proves the document surface's own relation
// set (viewer/editor only) is enforced independently of the tenant set.
func TestACLGrantDocument_BadRelation(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("should not be called")}
	h := &aclAPIHandler{memory: fake}
	did := uuid.New()

	req := grantReq(http.MethodPost, "/documents/"+did.String()+"/grants", `{"email":"a@example.com","relation":"manager"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLGrantDocument_NotAuthorized proves a caller who doesn't manage the
// doc's owning tenant gets 403.
func TestACLGrantDocument_NotAuthorized(t *testing.T) {
	fake := &fakeACLService{err: fmt.Errorf("%w: not authorized to manage document", apperr.ErrInvalidInput)}
	h := &aclAPIHandler{memory: fake}
	did := uuid.New()

	req := grantReq(http.MethodPost, "/documents/"+did.String()+"/grants", `{"email":"a@example.com","relation":"editor"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLListTenantGrants_NotAuthorized proves the list route also maps a
// ceiling denial to 403.
func TestACLListTenantGrants_NotAuthorized(t *testing.T) {
	fake := &fakeACLService{tenantGrantErr: fmt.Errorf("%w: not authorized to list grants for tenant", apperr.ErrInvalidInput)}
	h := &aclAPIHandler{memory: fake}
	tid := uuid.New()

	req := httptest.NewRequest(http.MethodGet, "/tenants/"+tid.String()+"/grants", nil)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestACLGrantTenant_InvalidID / TestACLGrantDocument_InvalidID prove malformed
// path ids are rejected 400 before any body decode or service call.
func TestACLGrantTenant_InvalidID(t *testing.T) {
	h := &aclAPIHandler{memory: &fakeACLService{}}
	req := grantReq(http.MethodPost, "/tenants/not-a-uuid/grants", `{"email":"a@example.com","relation":"viewer"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestACLGrantDocument_InvalidID(t *testing.T) {
	h := &aclAPIHandler{memory: &fakeACLService{}}
	req := grantReq(http.MethodPost, "/documents/not-a-uuid/grants", `{"email":"a@example.com","relation":"viewer"}`)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
