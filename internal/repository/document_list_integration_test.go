//go:build integration

package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

const listDim = 768

func openListPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	require.NoError(t, err)
	require.NoError(t, database.Migrate(db, "fake", "fake", listDim,
		database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()))
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func listTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "list-" + uuid.NewString()}
	require.NoError(t, db.Create(&ten).Error)
	return ten.ID
}

// seedListDoc inserts a document then forces its timestamps, so ordering tests
// control created_at/updated_at (GORM stamps now() on create otherwise).
func seedListDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, category, slug, title string, created, updated time.Time) {
	t.Helper()
	doc := &models.Document{ID: uuid.New(), TenantID: tenantID, Category: category, Slug: slug, Title: title, DocType: "reference"}
	require.NoError(t, db.Create(doc).Error)
	require.NoError(t, db.Exec("UPDATE documents SET created_at = ?, updated_at = ? WHERE id = ?", created, updated, doc.ID).Error)
}

func slugs(docs []models.Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Slug
	}
	return out
}

func TestList_SlugPrefix(t *testing.T) {
	db := openListPG(t)
	repo := repository.NewDocumentRepository(db)
	ten := listTenant(t, db)
	scope := []uuid.UUID{ten}
	now := time.Now()
	cat := "journal"
	for _, s := range []string{"2026-08-01", "2026-08-15", "2026-09-01", "2025-12-31"} {
		seedListDoc(t, db, ten, cat, s, "t", now, now)
	}
	// Literal-percent docs: a prefix "100%" must not act as a wildcard.
	seedListDoc(t, db, ten, "pfx", "100%-a", "t", now, now)
	seedListDoc(t, db, ten, "pfx", "100x-b", "t", now, now)

	month, err := repo.List(context.Background(), scope, &cat, nil, "2026-08", "slug", "asc", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"2026-08-01", "2026-08-15"}, slugs(month))

	year, err := repo.List(context.Background(), scope, &cat, nil, "2026", "slug", "asc", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"2026-08-01", "2026-08-15", "2026-09-01"}, slugs(year))

	pfx := "pfx"
	lit, err := repo.List(context.Background(), scope, &pfx, nil, `100\%`, "slug", "asc", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"100%-a"}, slugs(lit), "the % is literal, not a wildcard")
}

func TestList_OrderFieldsBothDirections(t *testing.T) {
	db := openListPG(t)
	repo := repository.NewDocumentRepository(db)
	ten := listTenant(t, db)
	scope := []uuid.UUID{ten}
	cat := "ord"
	t1 := time.Now().Add(-3 * time.Hour)
	t2 := time.Now().Add(-2 * time.Hour)
	t3 := time.Now().Add(-1 * time.Hour)
	// slug/title/created/updated each induce a distinct order.
	seedListDoc(t, db, ten, cat, "a", "Zebra", t1, t3)
	seedListDoc(t, db, ten, cat, "b", "Yak", t2, t2)
	seedListDoc(t, db, ten, cat, "c", "Xray", t3, t1)

	cases := []struct {
		orderBy, order string
		want           []string
	}{
		{"slug", "asc", []string{"a", "b", "c"}},
		{"slug", "desc", []string{"c", "b", "a"}},
		{"title", "asc", []string{"c", "b", "a"}},
		{"title", "desc", []string{"a", "b", "c"}},
		{"created_at", "asc", []string{"a", "b", "c"}},
		{"created_at", "desc", []string{"c", "b", "a"}},
		{"updated_at", "asc", []string{"c", "b", "a"}},
		{"updated_at", "desc", []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		got, err := repo.List(context.Background(), scope, &cat, nil, "", c.orderBy, c.order, 0, 0)
		require.NoError(t, err)
		require.Equal(t, c.want, slugs(got), "%s %s", c.orderBy, c.order)
	}
}

func TestList_PagingOverNonUniqueSortKeyIsTotal(t *testing.T) {
	db := openListPG(t)
	repo := repository.NewDocumentRepository(db)
	ten := listTenant(t, db)
	scope := []uuid.UUID{ten}
	cat := "dup"
	same := time.Now().Add(-time.Hour) // identical updated_at on every row
	const n = 7
	for i := 0; i < n; i++ {
		seedListDoc(t, db, ten, cat, "d"+string(rune('0'+i)), "t", same, same)
	}

	seen := map[uuid.UUID]int{}
	for off := 0; off < n+2; off += 2 {
		page, err := repo.List(context.Background(), scope, &cat, nil, "", "updated_at", "asc", 2, off)
		require.NoError(t, err)
		for _, d := range page {
			seen[d.ID]++
		}
	}
	require.Len(t, seen, n, "every row appears across the pages")
	for id, c := range seen {
		require.Equal(t, 1, c, "row %s appeared %d times — the id tiebreaker must make paging total", id, c)
	}
}
