//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// TestImportDocuments covers the three spec scenarios for the shared ingest
// core in one pass: a parseable document is stored and its document#tenant
// tuple seeded, an unparseable path is skipped (not fatal), and the import is
// scoped to the target tenant only.
func TestImportDocuments(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	svc := newAdminTestSvc(db, store)

	adminSubj := "admin-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(adminSubj)))
	adminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: adminSubj})

	target, err := svc.CreateTenant(adminCtx, "import-target-"+uuid.NewString(), "")
	require.NoError(t, err)
	other, err := svc.CreateTenant(adminCtx, "import-other-"+uuid.NewString(), "")
	require.NoError(t, err)

	slug := "gorm-" + uuid.NewString()
	src := func(emit func(path string, content []byte) error) error {
		if err := emit("learnings/go/"+slug+".md", []byte("# Title\n\nbody")); err != nil {
			return err
		}
		// Trims to "" via parseImportPath -> category/slug both empty -> skipped.
		return emit(".md", []byte("unparseable"))
	}

	result, err := svc.ImportDocuments(context.Background(), target.ID, src)
	require.NoError(t, err)
	require.Equal(t, service.ImportResult{Imported: 1, Skipped: 1}, result)

	docs := repository.NewDocumentRepository(db)
	subcat := "go"

	doc, err := docs.GetByPath(context.Background(), repository.ReadTenants(target.ID), target.ID, "learnings", &subcat, slug)
	require.NoError(t, err, "document must be stored under the target tenant")
	require.Equal(t, target.ID, doc.TenantID)
	requireTuple(t, store, authzseed.DocumentTenantEdge(doc.ID, target.ID))

	// Tenant-scoping: the import must not be visible from another tenant.
	_, err = docs.GetByPath(context.Background(), repository.ReadTenants(other.ID), other.ID, "learnings", &subcat, slug)
	require.Error(t, err, "import must not leak into a different tenant")
}
