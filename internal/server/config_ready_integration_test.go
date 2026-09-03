//go:build integration

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/globalconfig"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// fakeListener drives the readiness handler's listener-health input deterministically.
type fakeListener struct{ healthy atomic.Bool }

func (f *fakeListener) Healthy() bool { return f.healthy.Load() }

func statusOf(t *testing.T, h http.HandlerFunc, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

func setRequireListener(t *testing.T, repo *repository.InstanceConfigRepository, gc *globalconfig.Accessor, v bool) {
	t.Helper()
	require.NoError(t, repo.Update(context.Background(), models.InstanceConfigPatch{RequireConfigListener: &v}))
	require.NoError(t, gc.Refresh(context.Background()))
}

func TestReadiness_ConfigListenerToggle(t *testing.T) {
	db := openAPIPG(t)
	repo := repository.NewInstanceConfigRepository(db)
	gc := globalconfig.New(repo)
	require.NoError(t, gc.Load(context.Background()))
	t.Cleanup(func() { setRequireListener(t, repo, gc, false) })

	lst := &fakeListener{}
	ready := readyHandler(db, gc, lst)

	// Flag off (default): a dead listener does not fail readiness; health is green.
	setRequireListener(t, repo, gc, false)
	lst.healthy.Store(false)
	require.Equal(t, http.StatusOK, statusOf(t, ready, "/~/ready"))
	require.Equal(t, http.StatusOK, statusOf(t, healthHandler, "/~/health"))

	// Flag on + dead listener: not ready; health still green.
	setRequireListener(t, repo, gc, true)
	require.Equal(t, http.StatusServiceUnavailable, statusOf(t, ready, "/~/ready"))
	require.Equal(t, http.StatusOK, statusOf(t, healthHandler, "/~/health"))

	// Recovery: a reconnected listener clears it.
	lst.healthy.Store(true)
	require.Equal(t, http.StatusOK, statusOf(t, ready, "/~/ready"))
}

func TestRequireConfigListener_SeedOnceAndPatchSurvives(t *testing.T) {
	db := openAPIPG(t)
	repo := repository.NewInstanceConfigRepository(db)

	// Fresh-instance boot with the env flag on seeds it true exactly once.
	require.NoError(t, db.Exec("UPDATE instance_config SET globals_seeded = false").Error)
	seed := database.BaselineGlobalConfigDefaults()
	seed.RequireConfigListener = true
	migrate := func() {
		require.NoError(t, database.Migrate(db, "fake", "fake", apiTestDim,
			database.TenantColumnDefaults{StalenessMode: "off"}, seed))
	}
	migrate()
	cfg, err := repo.Get(context.Background())
	require.NoError(t, err)
	require.True(t, cfg.RequireConfigListener)

	// An admin turns it off; a restart (re-migrate, env still on) must not clobber it.
	off := false
	require.NoError(t, repo.Update(context.Background(), models.InstanceConfigPatch{RequireConfigListener: &off}))
	migrate()
	cfg, err = repo.Get(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.RequireConfigListener, "admin patch survived restart")
}
