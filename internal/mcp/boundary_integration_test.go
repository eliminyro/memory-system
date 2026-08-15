//go:build integration

package mcp

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

const boundaryDim = 768

func openBoundaryPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", boundaryDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func boundaryCtx(tid uuid.UUID, subj string) context.Context {
	c := auth.WithTenantID(context.Background(), tid)
	return auth.WithSubject(c, auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
}

func boundaryTenant(t *testing.T, db *gorm.DB, store authz.Store) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "mcp-" + uuid.NewString()}
	if err := db.Create(&ten).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := store.Write(context.Background(), authzseed.TenantSystemEdge(ten.ID)); err != nil {
		t.Fatalf("seed tenant edge: %v", err)
	}
	return ten.ID
}

// TestMCPBoundaryAdminGating drives the tool handlers with a real MemoryService +
// authz engine, asserting the admin-only surface is enforced via the tuple Check:
// a non-admin can't use the tenant_id override or an admin tool, a global admin can.
func TestMCPBoundaryAdminGating(t *testing.T) {
	db := openBoundaryPG(t)
	ctx := context.Background()
	store := authz.NewPostgresStore(db)

	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(boundaryDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewThresholdStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		repository.NewRecallReceiptRepository(db),
		store,
	)
	engine := authz.NewEngine(store)
	srv := NewServer(svc, engine)

	// A global admin subject (system:memory#admin) and a plain member subject.
	adminSubj := "admin-" + uuid.NewString()
	if err := store.Write(ctx, authzseed.SystemAdmin(adminSubj)); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	nonAdminSubj := "user-" + uuid.NewString()

	tenantA := boundaryTenant(t, db, store)
	tenantB := boundaryTenant(t, db, store)
	tenantBStr := tenantB.String()

	adminCtx := boundaryCtx(tenantA, adminSubj)
	nonAdminCtx := boundaryCtx(tenantA, nonAdminSubj)

	// Seed a document in tenant B by having the admin override tenant_id to B.
	slugB := "doc-" + uuid.NewString()
	if _, err := svc.StoreDocument(adminCtx, "learnings", nil, slugB, "# Title\n\n## Heading\nbody", true, "seed", &tenantB); err != nil {
		t.Fatalf("seed doc in tenant B: %v", err)
	}

	t.Run("non-admin tenant_id override denied", func(t *testing.T) {
		res, _, err := srv.GetDocument(nonAdminCtx, nil, GetDocumentInput{
			Category: "learnings",
			Slug:     slugB,
			TenantID: &tenantBStr,
		})
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("non-admin tenant_id override must be denied (IsError), got %+v", res)
		}
	})

	t.Run("admin tenant_id override allowed", func(t *testing.T) {
		res, _, err := srv.GetDocument(adminCtx, nil, GetDocumentInput{
			Category: "learnings",
			Slug:     slugB,
			TenantID: &tenantBStr,
		})
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if res == nil || res.IsError {
			t.Fatalf("admin tenant_id override must succeed, got %+v", res)
		}
	})

	t.Run("non-admin admin-tool denied", func(t *testing.T) {
		res, _, err := srv.ListTenants(nonAdminCtx, nil, ListTenantsInput{})
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("non-admin list_tenants must be denied (IsError), got %+v", res)
		}
	})

	t.Run("admin admin-tool allowed", func(t *testing.T) {
		res, _, err := srv.ListTenants(adminCtx, nil, ListTenantsInput{})
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if res == nil || res.IsError {
			t.Fatalf("admin list_tenants must succeed, got %+v", res)
		}
	})

	t.Run("isAdmin split resolves through the Check", func(t *testing.T) {
		if !srv.isAdmin(adminCtx) {
			t.Error("admin subject must resolve to the admin surface")
		}
		if srv.isAdmin(nonAdminCtx) {
			t.Error("non-admin subject must not resolve to the admin surface")
		}
	})
}
