package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// tenantLister is the slice of MemoryService the GET /api/tenants handler needs.
// An interface (like importJobStore) so handler unit tests can stub the listing
// without a database; the concrete *service.MemoryService satisfies it.
type tenantLister interface {
	ListTenantsByType(ctx context.Context, tenantType, q string) ([]service.TenantAccess, error)
}

// tenantCreator is the slice of MemoryService the admin create-tenant handler
// needs, an interface for the same stub-without-a-database reason. CreateTenant's
// optional type is variadic, so an omitted type falls through to the service
// default (shared).
type tenantCreator interface {
	CreateTenant(ctx context.Context, name, email string, tenantType ...string) (*models.Tenant, error)
}

// tenantListItem is one GET /api/tenants element (design.md §5): the manager UI
// only needs the tenant id/name/type plus the caller's effective relation, so
// this deliberately omits the email + retention settings that
// service.TenantAccess / models.Tenant otherwise carry — this endpoint is not
// adminOnly, so it must not leak per-tenant settings to delegated managers.
type tenantListItem struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Relation string    `json:"relation"`
}

// listTenantsByType backs GET /api/tenants?type=<t>&q=<filter> (design.md §5):
// a system admin sees all tenants of that type; a non-admin sees only the
// tenants they manage (the WritableTenants shape, enforced in-service). type +
// optional q filtering is applied server-side. Not adminOnly — delegated
// managers use it. An invalid type surfaces as 400 (ErrInvalidInput); other
// errors go through writeErr (which hides internal detail).
func listTenantsByType(w http.ResponseWriter, r *http.Request, lister tenantLister) {
	q := r.URL.Query()
	access, err := lister.ListTenantsByType(r.Context(), q.Get("type"), q.Get("q"))
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]tenantListItem, 0, len(access))
	for _, a := range access {
		items = append(items, tenantListItem{
			ID:       a.Tenant.ID,
			Name:     a.Tenant.Name,
			Type:     a.Tenant.Type,
			Relation: a.Relation,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// createTenantWithType backs POST /api/admin/tenants: decode name/email plus an
// optional type. An empty/omitted type is not forwarded, so the service applies
// its default (shared); an invalid type surfaces as 400 via writeErr
// (ErrInvalidInput). Extracted as a free function so a handler unit test can
// drive it with a stub tenantCreator (the method below wires the real service).
func createTenantWithType(w http.ResponseWriter, r *http.Request, creator tenantCreator) {
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Type  string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var (
		tenant *models.Tenant
		err    error
	)
	if body.Type != "" {
		tenant, err = creator.CreateTenant(r.Context(), body.Name, body.Email, body.Type)
	} else {
		tenant, err = creator.CreateTenant(r.Context(), body.Name, body.Email)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tenant)
}
