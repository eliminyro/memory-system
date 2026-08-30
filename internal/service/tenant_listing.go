package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
)

// ListTenantsByType backs GET /api/tenants?type=<t>&q=<filter> (design D5). It
// reuses the WritableTenants authz shape: a system admin sees all tenants
// (labeled admin), a non-admin sees only the tenants they manage (ReadBySubject
// candidates confirmed with Check(tenant, id, manager)). The result is then
// filtered by type and the optional q. An empty type means "all types"; a
// non-empty type must be valid (personal|shared) or ErrInvalidInput is returned
// so the handler can map it to 400. Not admin-only — managers use it.
func (s *MemoryService) ListTenantsByType(ctx context.Context, tenantType, q string) ([]TenantAccess, error) {
	if tenantType != "" && !models.IsValidTenantType(tenantType) {
		return nil, fmt.Errorf("%w: tenant type must be personal or shared", apperr.ErrInvalidInput)
	}
	all, err := s.WritableTenants(ctx)
	if err != nil {
		return nil, err
	}
	return filterTenantAccess(all, tenantType, q), nil
}

// filterTenantAccess is the pure type+q filter applied server-side. type (when
// non-empty) keeps only tenants of that type. q (when non-empty) keeps only
// tenants whose name contains q (case-insensitive substring) OR whose id equals
// q parsed as a tenant UUID. It always returns a non-nil slice so the JSON
// response serializes as [] rather than null.
func filterTenantAccess(all []TenantAccess, tenantType, q string) []TenantAccess {
	q = strings.TrimSpace(q)
	qLower := strings.ToLower(q)
	var qID uuid.UUID
	haveID := false
	if q != "" {
		if id, parseErr := uuid.Parse(q); parseErr == nil {
			qID, haveID = id, true
		}
	}
	out := make([]TenantAccess, 0, len(all))
	for _, ta := range all {
		if tenantType != "" && ta.Tenant.Type != tenantType {
			continue
		}
		if q != "" {
			nameHit := strings.Contains(strings.ToLower(ta.Tenant.Name), qLower)
			idHit := haveID && ta.Tenant.ID == qID
			if !nameHit && !idHit {
				continue
			}
		}
		out = append(out, ta)
	}
	return out
}
