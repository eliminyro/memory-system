//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestStoreDocument_ReStoreAtArchivedPath is the H3 regression: an archived doc
// keeps its (tenant, category, subcategory, slug) row, and before the unique
// path index was made partial on `archived_at IS NULL`, StoreDocument's Create
// branch hit a 23505 unique violation (opaque 500) when re-storing at that path.
// With the partial index the archived row no longer occupies the slot, so the
// re-store creates a fresh ACTIVE doc cleanly while the archived row survives.
func TestStoreDocument_ReStoreAtArchivedPath(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	tenant := models.Tenant{ID: uuid.New(), Name: "h3-" + uuid.NewString()}
	require.NoError(t, db.Create(&tenant).Error)

	// override=nil, so the tenant comes straight from the context.
	ctx := auth.WithLocalAdmin(auth.WithTenantID(context.Background(), tenant.ID))

	category := "learnings"
	slug := "h3-" + uuid.NewString()

	first, err := svc.StoreDocument(ctx, category, nil, slug, "# Title\n\noriginal body", true, "seed", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", first.Status)
	firstID := first.Document.ID

	// Archive it in place (mirrors what the retention sweep does).
	require.NoError(t, db.Model(&models.Document{}).
		Where("id = ?", firstID).
		Update("archived_at", time.Now()).Error)

	// Re-store at the SAME path: must succeed (Create branch, no 23505).
	second, err := svc.StoreDocument(ctx, category, nil, slug, "# Title\n\nfresh body", true, "re-store", nil, nil)
	require.NoError(t, err, "re-store at an archived path must not 23505")
	require.Equal(t, "ok", second.Status)
	require.NotEqual(t, firstID, second.Document.ID, "re-store must create a new active doc, not reuse the archived row")

	// A normal read returns the new active doc, not the archived one.
	docs := repository.NewDocumentRepository(db)
	got, err := docs.GetByPath(context.Background(), repository.ReadTenants(tenant.ID), tenant.ID, category, nil, slug)
	require.NoError(t, err)
	require.Equal(t, second.Document.ID, got.ID)
	require.Nil(t, got.ArchivedAt)

	// The archived row still exists (not overwritten/deleted).
	var archived models.Document
	require.NoError(t, db.Where("id = ?", firstID).First(&archived).Error)
	require.NotNil(t, archived.ArchivedAt, "the original row must remain archived")
}
