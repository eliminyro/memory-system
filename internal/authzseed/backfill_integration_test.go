//go:build integration

package authzseed_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
)

// openPG brings up the full memory-system schema (idempotently) against the
// shared TEST_DATABASE_URL. It never drops tables — tests isolate on fresh
// random UUIDs and scope their assertions accordingly, so the suite is safe to
// re-run against a persistent container. Run integration tests with -p 1 so
// packages don't write concurrently to the shared DB.
func openPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, 768, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func tupleKey(tp authz.Tuple) string {
	return tp.ObjectType + "|" + tp.ObjectID + "|" + tp.Relation + "|" +
		tp.SubjectType + "|" + tp.SubjectID + "|" + tp.SubjectRelation
}

// globalTupleSet snapshots every relation tuple as a key set. Valid for
// idempotency comparison only when nothing else writes between two snapshots.
func globalTupleSet(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	type row struct {
		ObjectType, ObjectID, Relation, SubjectType, SubjectID, SubjectRelation string
	}
	var rows []row
	if err := db.Table("relation_tuples").Find(&rows).Error; err != nil {
		t.Fatalf("read tuples: %v", err)
	}
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[tupleKey(authz.Tuple(r))] = true
	}
	return set
}

func mustCreate(t *testing.T, db *gorm.DB, v any) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("create %T: %v", v, err)
	}
}

func TestBackfill_IdempotentAndExpected(t *testing.T) {
	db := openPG(t)
	ctx := context.Background()
	store := authz.NewPostgresStore(db)

	tA := models.Tenant{ID: uuid.New(), Name: "a-" + uuid.NewString()}
	tB := models.Tenant{ID: uuid.New(), Name: "b-" + uuid.NewString()}
	mustCreate(t, db, &tA)
	mustCreate(t, db, &tB)

	tuMember := models.TenantUser{ID: uuid.New(), Email: "m-" + uuid.NewString() + "@x", TenantID: tA.ID, Role: models.TenantUserRoleMember}
	tuAdmin := models.TenantUser{ID: uuid.New(), Email: "a-" + uuid.NewString() + "@x", TenantID: tB.ID, Role: models.TenantUserRoleAdmin}
	mustCreate(t, db, &tuMember)
	mustCreate(t, db, &tuAdmin)

	dA := models.Document{ID: uuid.New(), TenantID: tA.ID, Category: "c", Slug: "s-" + uuid.NewString(), Title: "t"}
	dB := models.Document{ID: uuid.New(), TenantID: tB.ID, Category: "c", Slug: "s-" + uuid.NewString(), Title: "t"}
	mustCreate(t, db, &dA)
	mustCreate(t, db, &dB)

	kA := models.APIKey{ID: uuid.New(), TenantID: tA.ID, KeyHash: uuid.NewString(), Label: "k", Prefix: "p"}
	explicit := "tu-" + uuid.NewString()
	kB := models.APIKey{ID: uuid.New(), TenantID: tB.ID, KeyHash: uuid.NewString(), Label: "k", Prefix: "p", SubjectID: &explicit}
	mustCreate(t, db, &kA)
	mustCreate(t, db, &kB)

	if err := authzseed.Backfill(ctx, store, db); err != nil {
		t.Fatalf("backfill 1: %v", err)
	}
	set1 := globalTupleSet(t, db)
	if err := authzseed.Backfill(ctx, store, db); err != nil {
		t.Fatalf("backfill 2: %v", err)
	}
	set2 := globalTupleSet(t, db)

	// Idempotent: the second run adds/removes nothing.
	if len(set1) != len(set2) {
		t.Fatalf("not idempotent: %d tuples after run 1, %d after run 2", len(set1), len(set2))
	}
	for k := range set1 {
		if !set2[k] {
			t.Fatalf("tuple %q present after run 1 but not run 2", k)
		}
	}

	svcA := authz.ServicePrincipalID(tA.ID.String())
	svcB := authz.ServicePrincipalID(tB.ID.String())
	expect := []authz.Tuple{
		authzseed.CommonPoolViewerWildcard(),
		authzseed.TenantSystemEdge(tA.ID), authzseed.TenantMember(tA.ID, svcA),
		authzseed.TenantSystemEdge(tB.ID), authzseed.TenantMember(tB.ID, svcB),
		authzseed.TenantMember(tA.ID, tuMember.ID.String()),
		authzseed.TenantMember(tB.ID, tuAdmin.ID.String()),
		authzseed.TenantAdmin(tB.ID, tuAdmin.ID.String()),
		authzseed.DocumentTenantEdge(dA.ID, tA.ID),
		authzseed.DocumentTenantEdge(dB.ID, tB.ID),
		authzseed.TenantMember(tB.ID, explicit), // key B's explicit subject
	}
	for _, tp := range expect {
		if !set2[tupleKey(tp)] {
			t.Errorf("missing expected tuple %+v", tp)
		}
	}

	// No cross-tenant leakage.
	forbidden := []authz.Tuple{
		authzseed.TenantMember(tB.ID, tuMember.ID.String()),
		authzseed.TenantMember(tA.ID, tuAdmin.ID.String()),
		authzseed.TenantAdmin(tA.ID, tuAdmin.ID.String()),
		authzseed.TenantMember(tA.ID, explicit),
	}
	for _, tp := range forbidden {
		if set2[tupleKey(tp)] {
			t.Errorf("unexpected cross-tenant tuple %+v", tp)
		}
	}
}

func systemAdminSubjects(t *testing.T, store authz.Store) map[string]bool {
	t.Helper()
	got, err := store.ReadByObjectRelation(context.Background(), authz.TypeSystem, authz.SystemObjectID, authz.RelAdmin)
	if err != nil {
		t.Fatalf("read system admins: %v", err)
	}
	set := make(map[string]bool, len(got))
	for _, tp := range got {
		set[tp.SubjectID] = true
	}
	return set
}

func TestBootstrapAdmins_SeedsConfiguredSkipsUnknown(t *testing.T) {
	db := openPG(t)
	ctx := context.Background()
	store := authz.NewPostgresStore(db)

	tid := uuid.New()
	mustCreate(t, db, &models.Tenant{ID: tid, Name: "adm-" + uuid.NewString()})
	adminEmail := "admin-" + uuid.NewString() + "@x"
	tu := models.TenantUser{ID: uuid.New(), Email: adminEmail, TenantID: tid, Role: models.TenantUserRoleAdmin}
	mustCreate(t, db, &tu)

	before := systemAdminSubjects(t, store)
	ghost := "ghost-" + uuid.NewString() + "@x"
	if err := authzseed.BootstrapAdmins(ctx, store, db, []string{adminEmail, ghost, ""}, nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	after := systemAdminSubjects(t, store)

	// The configured email with a tenant_user was seeded.
	if !after[tu.ID.String()] {
		t.Fatalf("expected system admin for %s (subject %s)", adminEmail, tu.ID)
	}
	// Exactly one new subject was added (ghost@x with no tenant_user was skipped).
	if len(after) != len(before)+1 {
		t.Fatalf("expected exactly 1 new system admin (ghost skipped), before=%d after=%d", len(before), len(after))
	}

	// Idempotent: a second run adds nothing.
	if err := authzseed.BootstrapAdmins(ctx, store, db, []string{adminEmail, ghost, ""}, nil); err != nil {
		t.Fatalf("bootstrap 2: %v", err)
	}
	again := systemAdminSubjects(t, store)
	if len(again) != len(after) {
		t.Fatalf("second bootstrap not idempotent: %d -> %d", len(after), len(again))
	}
}
