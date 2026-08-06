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
	"github.com/eliminyro/memory-system/internal/service"
)

// TestTenantSettings_ManagerReadAndWrite proves a tenant#manager may read the
// settings (200, DTO shape) and, under the default open policy, persist a toggle
// change (200 + persisted).
func TestTenantSettings_ManagerReadAndWrite(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "set-mgr-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	subj := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, subj)))
	base := "/tenants/" + tenant.ID.String() + "/settings"

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, base, userCtx(tenant.ID, subj), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got tenantSettingsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, tenant.ID, got.ID)
	require.Equal(t, models.StalenessModeOff, got.StalenessMode)
	require.Equal(t, models.SelfServicePolicyOpen, got.EffectiveSelfServicePolicy)

	recW := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recW, ctxJSONReq(http.MethodPatch, base, userCtx(tenant.ID, subj), map[string]any{
		"staleness_mode": models.StalenessModeAdvisory, "duplicate_guard": true,
	}))
	require.Equal(t, http.StatusOK, recW.Code, recW.Body.String())
	var updated tenantSettingsResponse
	require.NoError(t, json.Unmarshal(recW.Body.Bytes(), &updated))
	require.Equal(t, models.StalenessModeAdvisory, updated.StalenessMode)
	require.True(t, updated.DuplicateGuard)

	// Persisted: an admin read-back reflects the change.
	persisted, err := f.svc.UpdateTenantSettings(f.adminCtx, tenant.ID, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, models.StalenessModeAdvisory, persisted.StalenessMode)
	require.True(t, persisted.DuplicateGuard)
}

// TestTenantSettings_SystemAdminReadAndWrite proves a system admin reads and
// writes settings on any tenant regardless of policy.
func TestTenantSettings_SystemAdminReadAndWrite(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "set-adm-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)
	base := "/tenants/" + tenant.ID.String() + "/settings"

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, base, adminReqCtx(tenant.ID), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	recW := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recW, ctxJSONReq(http.MethodPatch, base, adminReqCtx(tenant.ID), map[string]any{
		"cleanup_scan_enabled": true,
	}))
	require.Equal(t, http.StatusOK, recW.Code, recW.Body.String())
	var updated tenantSettingsResponse
	require.NoError(t, json.Unmarshal(recW.Body.Bytes(), &updated))
	require.True(t, updated.CleanupScanEnabled)
}

// TestTenantSettings_MemberReadRefused proves a plain member (no manage rights)
// cannot read settings: the CanManageTenant gate refuses, surfacing as 400
// (ErrInvalidInput via writeErr) with no settings body.
func TestTenantSettings_MemberReadRefused(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "set-mem-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	memTU, err := f.svc.GrantTenantUser(f.adminCtx, "mem-"+uuid.NewString()+"@example.com", tenant.ID, models.TenantUserRoleMember)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, "/tenants/"+tenant.ID.String()+"/settings",
		userCtx(tenant.ID, memTU.ID.String()), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestTenantSettings_AdminOnlyLock proves the self-service lock: under
// admin_only, a non-admin manager may still READ but its WRITE is refused (400),
// while a system admin write is allowed.
func TestTenantSettings_AdminOnlyLock(t *testing.T) {
	f := newWriteAPIFixture(t)
	tenant, err := f.svc.CreateTenant(f.adminCtx, "set-lock-"+uuid.NewString(), "", models.TenantTypeShared)
	require.NoError(t, err)

	adminOnly := models.SelfServicePolicyAdminOnly
	_, err = f.svc.UpdateTenant(f.adminCtx, tenant.ID, service.UpdateTenantFields{SelfServicePolicy: &adminOnly})
	require.NoError(t, err)

	mgr := "mgr-" + uuid.NewString()
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantManager(tenant.ID, mgr)))
	base := "/tenants/" + tenant.ID.String() + "/settings"

	// Read still allowed via CanManageTenant, and reports the locked policy.
	recRead := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recRead, ctxJSONReq(http.MethodGet, base, userCtx(tenant.ID, mgr), nil))
	require.Equal(t, http.StatusOK, recRead.Code, recRead.Body.String())
	var got tenantSettingsResponse
	require.NoError(t, json.Unmarshal(recRead.Body.Bytes(), &got))
	require.Equal(t, models.SelfServicePolicyAdminOnly, got.EffectiveSelfServicePolicy)

	// Write refused: the lock escalates the gate to admin.
	recW := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recW, ctxJSONReq(http.MethodPatch, base, userCtx(tenant.ID, mgr), map[string]any{
		"staleness_mode": models.StalenessModeHard,
	}))
	require.Equal(t, http.StatusBadRequest, recW.Code, recW.Body.String())

	// System admin still allowed.
	recAdm := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recAdm, ctxJSONReq(http.MethodPatch, base, adminReqCtx(tenant.ID), map[string]any{
		"staleness_mode": models.StalenessModeHard,
	}))
	require.Equal(t, http.StatusOK, recAdm.Code, recAdm.Body.String())
}
