package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// stubTenantSvc stands in for MemoryService in the tenant list/create handler
// unit tests (mirrors fakeImportJobs). ListTenantsByType reproduces the service
// contract in memory — type validation, the admin-vs-managed authz shape, and
// the type + q (name substring OR exact UUID) filter — so the enumerated
// design.md §5 scenarios can be driven through the real handler without a
// database. CreateTenant records what the handler forwarded.
type stubTenantSvc struct {
	admin bool
	all   []stubTenantEntry

	// CreateTenant capture.
	createName       string
	createOwnerEmail string
	createType       []string
	createErr        error
}

type stubTenantEntry struct {
	access  service.TenantAccess
	managed bool // visible to a non-admin caller (they manage it)
}

func (s *stubTenantSvc) ListTenantsByType(_ context.Context, tenantType, q string) ([]service.TenantAccess, error) {
	if tenantType != "" && !models.IsValidTenantType(tenantType) {
		return nil, fmt.Errorf("%w: tenant type must be personal or shared", apperr.ErrInvalidInput)
	}
	var out []service.TenantAccess
	for _, e := range s.all {
		if !s.admin && !e.managed {
			continue
		}
		if tenantType != "" && e.access.Tenant.Type != tenantType {
			continue
		}
		if q != "" && !tenantMatchesQ(e.access.Tenant, q) {
			continue
		}
		out = append(out, e.access)
	}
	return out, nil
}

func (s *stubTenantSvc) CreateTenant(_ context.Context, name, ownerEmail string, tenantType ...string) (*models.Tenant, error) {
	s.createName, s.createOwnerEmail, s.createType = name, ownerEmail, tenantType
	if s.createErr != nil {
		return nil, s.createErr
	}
	typ := models.TenantTypeShared
	if len(tenantType) > 0 && tenantType[0] != "" {
		if !models.IsValidTenantType(tenantType[0]) {
			return nil, fmt.Errorf("%w: invalid tenant type", apperr.ErrInvalidInput)
		}
		typ = tenantType[0]
	}
	return &models.Tenant{ID: uuid.New(), Name: name, Type: typ}, nil
}

func tenantMatchesQ(t models.Tenant, q string) bool {
	if strings.Contains(strings.ToLower(t.Name), strings.ToLower(q)) {
		return true
	}
	return t.ID.String() == q
}

// tenantUniverse is a fixed 3-tenant fixture: two shared (one the caller only
// sees as a sysadmin, one they manage) and one personal.
func tenantUniverse() (entries []stubTenantEntry, acmeID, betaID, adaID uuid.UUID) {
	acmeID, betaID, adaID = uuid.New(), uuid.New(), uuid.New()
	entries = []stubTenantEntry{
		{access: service.TenantAccess{Tenant: models.Tenant{ID: acmeID, Name: "Acme Shared", Type: models.TenantTypeShared}, Relation: "admin"}, managed: false},
		{access: service.TenantAccess{Tenant: models.Tenant{ID: betaID, Name: "Beta Shared", Type: models.TenantTypeShared}, Relation: "manager"}, managed: true},
		{access: service.TenantAccess{Tenant: models.Tenant{ID: adaID, Name: "Ada Personal", Type: models.TenantTypePersonal}, Relation: "admin"}, managed: false},
	}
	return entries, acmeID, betaID, adaID
}

func doListTenants(svc tenantLister, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/tenants"+query, nil)
	rec := httptest.NewRecorder()
	listTenantsByType(rec, req, svc)
	return rec
}

func decodeTenantItems(t *testing.T, rec *httptest.ResponseRecorder) []tenantListItem {
	t.Helper()
	var items []tenantListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("response not a JSON array of items: %v (body=%s)", err, rec.Body.String())
	}
	return items
}

func TestListTenants_AdminSeesAllOfType(t *testing.T) {
	entries, _, _, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: true, all: entries}, "?type=shared")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items := decodeTenantItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("admin got %d shared tenants, want 2: %+v", len(items), items)
	}
}

func TestListTenants_ManagerSeesOnlyManaged(t *testing.T) {
	entries, _, betaID, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: false, all: entries}, "?type=shared")
	items := decodeTenantItems(t, rec)
	if len(items) != 1 || items[0].ID != betaID {
		t.Fatalf("manager got %+v, want only the managed Beta tenant %s", items, betaID)
	}
	if items[0].Relation != "manager" {
		t.Errorf("relation = %q, want manager", items[0].Relation)
	}
}

func TestListTenants_QueryNarrowsByNameSubstring(t *testing.T) {
	entries, _, betaID, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: true, all: entries}, "?q=beta")
	items := decodeTenantItems(t, rec)
	if len(items) != 1 || items[0].ID != betaID {
		t.Fatalf("q=beta got %+v, want only Beta %s", items, betaID)
	}
}

func TestListTenants_QueryNarrowsByExactUUID(t *testing.T) {
	entries, acmeID, _, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: true, all: entries}, "?q="+acmeID.String())
	items := decodeTenantItems(t, rec)
	if len(items) != 1 || items[0].ID != acmeID {
		t.Fatalf("q=<acmeID> got %+v, want only Acme %s", items, acmeID)
	}
}

func TestListTenants_BadTypeIs400(t *testing.T) {
	entries, _, _, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: true, all: entries}, "?type=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid type (%s)", rec.Code, rec.Body.String())
	}
}

// The list projection carries only id/name/type/relation — never the email or
// retention settings that models.Tenant otherwise holds (this endpoint is not
// adminOnly).
func TestListTenants_ProjectionOmitsSettings(t *testing.T) {
	entries, _, _, _ := tenantUniverse()
	rec := doListTenants(&stubTenantSvc{admin: true, all: entries}, "?q=Acme")
	var raw []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("want 1 item, got %d", len(raw))
	}
	for _, leaked := range []string{"email", "staleness_mode", "tenant"} {
		if _, ok := raw[0][leaked]; ok {
			t.Errorf("field %q leaked into the tenant list projection: %v", leaked, raw[0])
		}
	}
	if raw[0]["relation"] != "admin" || raw[0]["type"] != models.TenantTypeShared {
		t.Errorf("projection = %v, want relation=admin type=shared", raw[0])
	}
}

func doCreateTenant(svc tenantCreator, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/tenants", strings.NewReader(body))
	rec := httptest.NewRecorder()
	createTenantWithType(rec, req, svc)
	return rec
}

// An omitted type is not forwarded to CreateTenant (empty variadic), so the
// service default (shared) applies.
func TestCreateTenant_DefaultTypeOmitsArg(t *testing.T) {
	svc := &stubTenantSvc{}
	rec := doCreateTenant(svc, `{"name":"t","owner_email":"e@x.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if len(svc.createType) != 0 {
		t.Errorf("createType = %v, want no variadic arg forwarded", svc.createType)
	}
	if svc.createOwnerEmail != "e@x.com" {
		t.Errorf("createOwnerEmail = %q, want e@x.com (owner_email forwarded)", svc.createOwnerEmail)
	}
	var got models.Tenant
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not a tenant: %v", err)
	}
	if got.Type != models.TenantTypeShared {
		t.Errorf("type = %q, want shared", got.Type)
	}
}

// An explicit type is forwarded verbatim and persists on the created tenant.
func TestCreateTenant_ExplicitTypeForwarded(t *testing.T) {
	svc := &stubTenantSvc{}
	rec := doCreateTenant(svc, `{"name":"t","type":"personal","owner_email":"e@x.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if len(svc.createType) != 1 || svc.createType[0] != models.TenantTypePersonal {
		t.Fatalf("createType = %v, want [personal]", svc.createType)
	}
	var got models.Tenant
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not a tenant: %v", err)
	}
	if got.Type != models.TenantTypePersonal {
		t.Errorf("type = %q, want personal", got.Type)
	}
}

// A personal tenant without an owner email is rejected at the edge (the
// personal-owner invariant), never reaching the service.
func TestCreateTenant_PersonalRequiresOwnerEmail(t *testing.T) {
	svc := &stubTenantSvc{}
	rec := doCreateTenant(svc, `{"name":"t","type":"personal"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if svc.createName != "" {
		t.Error("service CreateTenant must not be called on a rejected request")
	}
}

// An invalid type surfaces as 400 (the service's ErrInvalidInput mapped by
// writeErr).
func TestCreateTenant_InvalidTypeIs400(t *testing.T) {
	rec := doCreateTenant(&stubTenantSvc{}, `{"name":"t","type":"bogus"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid type (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateTenant_InvalidBodyIs400(t *testing.T) {
	rec := doCreateTenant(&stubTenantSvc{}, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a malformed body", rec.Code)
	}
}
