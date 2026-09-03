//go:build integration

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

func lp[T any](v T) *T { return &v }

type listEntry struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Title string `json:"title"`
}

func decodeListDocs(t *testing.T, srv *Server, ctx context.Context, in ListDocumentsInput) []listEntry {
	t.Helper()
	res, _, err := srv.ListDocuments(ctx, nil, in)
	require.NoError(t, err)
	require.False(t, res.IsError, "unexpected tool error: %s", resultText(t, res))
	var out []listEntry
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &out))
	return out
}

func TestMCPListDocuments_PrefixOrderPaging(t *testing.T) {
	db := openBoundaryPG(t)
	store := authz.NewPostgresStore(db)
	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(boundaryDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewPolicyStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		nil, nil,
		store,
	)
	srv := NewServer(svc, authz.NewEngine(store))
	ten := boundaryTenant(t, db, store)
	ctx := boundaryCtx(ten, "user-"+uuid.NewString())

	// Unique category so the fresh tenant's read scope (incl. common pool) can't
	// bleed other tests' docs into the assertions.
	cat := "jrnl-" + uuid.NewString()[:8]
	var paths []string
	for i := 1; i <= 10; i++ {
		slug := fmt.Sprintf("2026-01-%02d", i)
		_, err := svc.StoreDocument(ctx, cat, nil, slug, "# T\n\n## H\nbody", true, "seed", nil, nil)
		require.NoError(t, err)
		paths = append(paths, cat+"/"+slug)
	}

	// Omitting the new params returns the seeded set in slug-ascending order.
	all := decodeListDocs(t, srv, ctx, ListDocumentsInput{Category: &cat})
	require.Len(t, all, 10)
	got := make([]string, len(all))
	for i, e := range all {
		got[i] = e.Path
	}
	require.Equal(t, paths, got, "default order is slug ascending")

	// desc + limit=7 returns the seven most recent, newest first.
	recent := decodeListDocs(t, srv, ctx, ListDocumentsInput{Category: &cat, OrderBy: lp("slug"), Order: lp("desc"), Limit: lp(7)})
	require.Len(t, recent, 7)
	require.Equal(t, cat+"/2026-01-10", recent[0].Path)
	require.Equal(t, cat+"/2026-01-04", recent[6].Path)

	// A month-day prefix narrows without listing everything (2026-01-01..09).
	prefixed := decodeListDocs(t, srv, ctx, ListDocumentsInput{Category: &cat, SlugPrefix: lp("2026-01-0")})
	require.Len(t, prefixed, 9)

	// Invalid order_by is rejected at the tool.
	res, _, err := srv.ListDocuments(ctx, nil, ListDocumentsInput{Category: &cat, OrderBy: lp("name")})
	require.NoError(t, err)
	require.True(t, res.IsError, "invalid order_by must be a tool error")
}
