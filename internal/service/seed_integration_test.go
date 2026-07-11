//go:build integration

package service_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

const fakeDim = 768

func openServicePG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", fakeDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func requireTuple(t *testing.T, store authz.Store, want authz.Tuple) {
	t.Helper()
	got, err := store.ReadByObjectRelation(context.Background(), want.ObjectType, want.ObjectID, want.Relation)
	require.NoError(t, err)
	for _, g := range got {
		if g == want {
			return
		}
	}
	t.Fatalf("tuple %+v not found; have %+v", want, got)
}

// TestLifecycleSeeding proves each create path writes the expected tuple(s).
func TestLifecycleSeeding(t *testing.T) {
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)

	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil,
		store,
	)

	// Admin is decided by the tuple Check now, not an email allowlist: seed a
	// global-admin subject and carry it on the request context.
	adminSubj := "admin-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.SystemAdmin(adminSubj)))
	adminCtx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: adminSubj})

	// CreateTenant -> system parent edge + service-principal membership.
	tenant, err := svc.CreateTenant(adminCtx, "acme-"+uuid.NewString(), "")
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantSystemEdge(tenant.ID))
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, authz.ServicePrincipalID(tenant.ID.String())))

	// StoreDocument -> document#tenant parent edge.
	docCtx := auth.WithTenantID(adminCtx, tenant.ID)
	res, err := svc.StoreDocument(docCtx, "learnings", nil, "seed-"+uuid.NewString(), "# Title\n\nbody", true, "test", nil)
	require.NoError(t, err)
	require.NotNil(t, res.Document)
	requireTuple(t, store, authzseed.DocumentTenantEdge(res.Document.ID, tenant.ID))

	// CreateAPIKey with an explicit subject -> that subject's membership.
	sub := "tu-" + uuid.NewString()
	_, _, err = svc.CreateAPIKey(adminCtx, tenant.ID, "key", &sub)
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, sub))

	// GrantTenantUser (admin) -> membership + admin.
	tu, err := svc.GrantTenantUser(adminCtx, "user-"+uuid.NewString()+"@x", tenant.ID, "admin")
	require.NoError(t, err)
	requireTuple(t, store, authzseed.TenantMember(tenant.ID, tu.ID.String()))
	requireTuple(t, store, authzseed.TenantAdmin(tenant.ID, tu.ID.String()))
}
