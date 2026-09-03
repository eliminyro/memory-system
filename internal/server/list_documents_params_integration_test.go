//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

func restListFixture(t *testing.T) (*apiHandler, uuid.UUID, context.Context) {
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
		repository.NewLintRepository(db),
		staleness.NewPolicyStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		nil, nil,
		store,
	)
	ctx := context.Background()
	ten := models.Tenant{ID: uuid.New(), Name: "rlist-" + uuid.NewString()}
	require.NoError(t, db.Create(&ten).Error)
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(ten.ID)))
	subj := "user-" + uuid.NewString()
	seedCtx := auth.WithSubject(auth.WithTenantID(ctx, ten.ID), auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
	return &apiHandler{memory: svc}, ten.ID, seedCtx
}

func restListSlugs(t *testing.T, h *apiHandler, tenant uuid.UUID, target string) (int, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, reqAs(http.MethodGet, target, tenant))
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var docs []models.Document
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &docs))
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Slug
	}
	return rec.Code, out
}

// TestRESTListDocuments_ParamsMatchTool drives the same ordering/prefix/paging
// the MCP tool test exercises through the REST browse endpoint — both share
// ValidateListOptions + ListDocuments, so results and the rejection stay in parity.
func TestRESTListDocuments_ParamsMatchTool(t *testing.T) {
	h, tenant, seedCtx := restListFixture(t)
	cat := "jrnl-" + uuid.NewString()[:8]
	for i := 1; i <= 10; i++ {
		slug := fmt.Sprintf("2026-01-%02d", i)
		_, err := h.memory.StoreDocument(seedCtx, cat, nil, slug, "# T\n\n## H\nbody", true, "seed", nil, nil)
		require.NoError(t, err)
	}

	// desc + limit=7 → seven most recent, newest first.
	code, recent := restListSlugs(t, h, tenant, "/documents?category="+cat+"&order_by=slug&order=desc&limit=7")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, recent, 7)
	require.Equal(t, "2026-01-10", recent[0])
	require.Equal(t, "2026-01-04", recent[6])

	// Month-day prefix narrows to 2026-01-01..09.
	code, prefixed := restListSlugs(t, h, tenant, "/documents?category="+cat+"&slug_prefix=2026-01-0&limit=200")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, prefixed, 9)

	// Invalid order_by is rejected with 400, matching the tool.
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, reqAs(http.MethodGet, "/documents?category="+cat+"&order_by=name", tenant))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
