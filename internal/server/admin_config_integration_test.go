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

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/globalconfig"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// configFixture is a real apiHandler with the global-config surface wired
// (instance_config repo + a refreshable accessor), plus admin and non-admin
// request contexts, to drive GET/PATCH /admin/config end to end.
type configFixture struct {
	h        *apiHandler
	gc       *globalconfig.Accessor
	admin    context.Context
	nonAdmin context.Context
}

func newConfigFixture(t *testing.T) *configFixture {
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
		nil, nil,
		store,
	)
	cfgRepo := repository.NewInstanceConfigRepository(db)
	gc := globalconfig.New(cfgRepo)
	require.NoError(t, gc.Load(context.Background()))
	return &configFixture{
		h:        &apiHandler{memory: svc, instanceConfig: cfgRepo, globalCfg: gc},
		gc:       gc,
		admin:    auth.WithLocalAdmin(context.Background()),
		nonAdmin: userCtx(uuid.New(), "nobody-"+uuid.NewString()),
	}
}

// getConfig drives GET /admin/config as ctx and decodes the response.
func (f *configFixture) getConfig(t *testing.T, ctx context.Context) models.InstanceConfig {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, "/admin/config", ctx, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cfg models.InstanceConfig
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
	return cfg
}

// TestAdminConfig_AdminReadsCurrentConfig proves an admin GET returns the
// singleton global config seeded from BaselineGlobalConfigDefaults at migrate time.
func TestAdminConfig_AdminReadsCurrentConfig(t *testing.T) {
	f := newConfigFixture(t)
	cfg := f.getConfig(t, f.admin)
	require.Equal(t, 20, cfg.CandidatePool)
	require.InDelta(t, 0.5, cfg.MMRLambda, 1e-9)
	require.InDelta(t, 0.85, cfg.DuplicateThreshold, 1e-9)
	require.Equal(t, models.SelfServicePolicyOpen, cfg.SelfServicePolicy)
	require.Positive(t, cfg.SnippetChars)
}

// TestAdminConfig_NonAdminRefused proves both config endpoints are admin-only:
// a non-admin GET and PATCH are refused with 403 at the adminOnly gate.
func TestAdminConfig_NonAdminRefused(t *testing.T) {
	f := newConfigFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodGet, "/admin/config", f.nonAdmin, nil))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	recP := httptest.NewRecorder()
	f.h.mux().ServeHTTP(recP, ctxJSONReq(http.MethodPatch, "/admin/config", f.nonAdmin, map[string]any{"candidate_pool": 30}))
	require.Equal(t, http.StatusForbidden, recP.Code, recP.Body.String())
}

// TestAdminConfig_PartialUpdatePersistsAndAppliesLive proves a partial PATCH
// updates only the supplied fields (round-trip GET), persists (DB value wins),
// and refreshes the cached accessor so the change applies without a restart.
func TestAdminConfig_PartialUpdatePersistsAndAppliesLive(t *testing.T) {
	f := newConfigFixture(t)
	before := f.getConfig(t, f.admin)

	const newPool, newLambda = 50, 0.3
	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPatch, "/admin/config", f.admin, map[string]any{
		"candidate_pool": newPool, "mmr_lambda": newLambda,
	}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The response echoes the refreshed config: changed fields applied, others kept.
	var resp models.InstanceConfig
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, newPool, resp.CandidatePool)
	require.InDelta(t, newLambda, resp.MMRLambda, 1e-9)
	require.Equal(t, before.SnippetChars, resp.SnippetChars, "untouched field must be unchanged")

	// A fresh read reflects the persisted values (stored value wins).
	after := f.getConfig(t, f.admin)
	require.Equal(t, newPool, after.CandidatePool)
	require.InDelta(t, newLambda, after.MMRLambda, 1e-9)
	require.Equal(t, before.SnippetChars, after.SnippetChars)

	// Live-apply: the cached accessor was refreshed, no restart needed.
	require.Equal(t, newPool, f.gc.CandidatePool())
	require.InDelta(t, newLambda, f.gc.MMRLambda(), 1e-9)
}

// TestAdminConfig_RetentionFieldsRoundTripAndValidate proves the retention
// instance_config fields PATCH round-trip (persist + live accessor) and that
// validation rejects a negative grace and a sub-1 metrics retention.
func TestAdminConfig_RetentionFieldsRoundTripAndValidate(t *testing.T) {
	f := newConfigFixture(t)

	rec := httptest.NewRecorder()
	f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPatch, "/admin/config", f.admin, map[string]any{
		"retention_sweep_enabled": true, "retention_grace_days": 14, "metrics_retention_days": 45,
	}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	after := f.getConfig(t, f.admin)
	require.True(t, after.RetentionSweepEnabled)
	require.Equal(t, 14, after.RetentionGraceDays)
	require.Equal(t, 45, after.MetricsRetentionDays)
	require.Equal(t, 14, f.gc.RetentionGraceDays())
	require.Equal(t, 45, f.gc.MetricsRetentionDays())

	for _, p := range []map[string]any{{"retention_grace_days": -1}, {"metrics_retention_days": 0}} {
		recB := httptest.NewRecorder()
		f.h.mux().ServeHTTP(recB, ctxJSONReq(http.MethodPatch, "/admin/config", f.admin, p))
		require.Equal(t, http.StatusBadRequest, recB.Code, recB.Body.String())
	}
}

// TestAdminConfig_InvalidValueRejectedAtomically proves an out-of-range field
// rejects the whole PATCH with 400 and applies nothing: a valid sibling field in
// the same request is not persisted either, and the accessor is untouched.
func TestAdminConfig_InvalidValueRejectedAtomically(t *testing.T) {
	f := newConfigFixture(t)
	before := f.getConfig(t, f.admin)

	cases := []struct {
		name  string
		patch map[string]any
	}{
		{"mmr_lambda out of range", map[string]any{"mmr_lambda": 2.0, "candidate_pool": 7}},
		{"candidate_pool zero", map[string]any{"candidate_pool": 0, "mmr_lambda": 0.9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.h.mux().ServeHTTP(rec, ctxJSONReq(http.MethodPatch, "/admin/config", f.admin, tc.patch))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			// Nothing applied: both fields (including the valid sibling) unchanged.
			after := f.getConfig(t, f.admin)
			require.Equal(t, before.CandidatePool, after.CandidatePool)
			require.InDelta(t, before.MMRLambda, after.MMRLambda, 1e-9)
			require.Equal(t, before.CandidatePool, f.gc.CandidatePool())
			require.InDelta(t, before.MMRLambda, f.gc.MMRLambda(), 1e-9)
		})
	}
}
