//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
)

// TestAdminTenantPatch_SelfServiceLock proves the admin-only lock setter
// (PATCH /admin/tenants/{id}): a system admin sets and clears self_service_policy
// and gets back the settings projection (with the resolved effective policy),
// while a non-admin — even a tenant manager — is refused by the adminOnly mux.
func TestAdminTenantPatch_SelfServiceLock(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "adm-lock-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	path := "/admin/tenants/" + tenant.ID.String()

	// Admin sets admin_only → stored policy + effective both admin_only.
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPatch, path, adminReqCtx(tenant.ID), map[string]any{
		"self_service_policy": models.SelfServicePolicyAdminOnly,
	}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got tenantSettingsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyAdminOnly, *got.SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyAdminOnly, got.EffectiveSelfServicePolicy)

	// Admin clears to inherit (NULL) → effective resolves to the global default (open).
	recClr := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recClr, ctxJSONReq(http.MethodPatch, path, adminReqCtx(tenant.ID), map[string]any{
		"self_service_policy": "inherit",
	}))
	require.Equal(t, http.StatusOK, recClr.Code, recClr.Body.String())
	var cleared tenantSettingsResponse
	require.NoError(t, json.Unmarshal(recClr.Body.Bytes(), &cleared))
	require.Nil(t, cleared.SelfServicePolicy)
	require.Equal(t, models.SelfServicePolicyOpen, cleared.EffectiveSelfServicePolicy)

	// A non-admin manager is refused by the adminOnly mux — the lock stays admin-only.
	mgr := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, mgr)))
	recF := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recF, ctxJSONReq(http.MethodPatch, path, userCtx(tenant.ID, mgr), map[string]any{
		"self_service_policy": models.SelfServicePolicyAdminOnly,
	}))
	require.Equal(t, http.StatusForbidden, recF.Code, recF.Body.String())
}
