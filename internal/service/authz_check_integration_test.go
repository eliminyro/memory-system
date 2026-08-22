//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// authzFixture wires a fully-populated MemoryService over the shared tuple store
// (the same *authz.Engine path production uses) plus test tenants, subjects, and
// docs — so authorization decisions are exercised end to end.
type authzFixture struct {
	db    *gorm.DB
	store authz.Store
	svc   *service.MemoryService

	tenantA uuid.UUID
	tenantB uuid.UUID
	tenantT uuid.UUID

	subjA string // member of tenant A, not admin
	subjB string // member of tenant B, not admin
	subjT string // tenant T service principal, not admin
	admin string // global admin (system:memory#admin)

	docA, secA        uuid.UUID
	docB, secB, docB2 uuid.UUID
	docC, secC, docC2 uuid.UUID // common/bootstrap pool
}

func ctxFor(tid uuid.UUID, subj string) context.Context {
	c := auth.WithTenantID(context.Background(), tid)
	return auth.WithSubject(c, auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
}

func newAuthzFixture(t *testing.T) *authzFixture {
	t.Helper()
	db := openServicePG(t)
	store := authz.NewPostgresStore(db)
	ctx := context.Background()

	svc := service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		service.NewFakeEmbedder(fakeDim),
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		repository.NewLintRepository(db),
		staleness.NewThresholdStore(db),
		repository.NewOverrideLogRepository(db),
		repository.NewCleanupQueueRepository(db),
		store,
	)

	f := &authzFixture{db: db, store: store, svc: svc}

	// Ensure the bootstrap/common tenant exists (resolveTenant override to it
	// calls tenants.GetByID) and carries the tuples backfill would have written.
	boot := models.Tenant{ID: models.BootstrapTenantID, Name: "common-pool"}
	require.NoError(t, db.Where("id = ?", models.BootstrapTenantID).FirstOrCreate(&boot).Error)
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(models.BootstrapTenantID)))
	require.NoError(t, store.Write(ctx, authzseed.CommonPoolViewerWildcard()))

	// Start every test from an empty common pool. The suite runs -p 1 and each
	// fixture seeds 2 pool docs (docC/docC2 below) readable by all tests via
	// CommonPoolViewerWildcard; without this they accumulate across the run, and
	// because the fake embedder ties every vector at ~0.74 similarity an
	// accumulated pool crowds unique-token search hits out of the top-K — making
	// search-based tests order-dependent (the recall + snippet flakes). This
	// runs before docC/docC2 are seeded, so it only clears prior tests' docs.
	require.NoError(t, resetCommonPool(db))

	// Tenant A + member subject.
	f.tenantA = f.mkTenant(t)
	f.subjA = "userA-" + uuid.NewString()
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(f.tenantA, f.subjA)))

	// Tenant B + member subject.
	f.tenantB = f.mkTenant(t)
	f.subjB = "userB-" + uuid.NewString()
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(f.tenantB, f.subjB)))

	// Tenant T + its service-principal subject (the escalation target).
	f.tenantT = f.mkTenant(t)
	f.subjT = authz.ServicePrincipalID(f.tenantT.String())
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(f.tenantT, f.subjT)))

	// Global admin subject.
	f.admin = "admin-" + uuid.NewString()
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(f.admin)))

	// Documents. StoreDocument creates the doc AND seeds its tenant edge, so the
	// editor-from-tenant rewrite resolves for members.
	f.docA, f.secA = f.storeDoc(t, ctxFor(f.tenantA, f.subjA), nil)
	f.docB, f.secB = f.storeDoc(t, ctxFor(f.tenantB, f.subjB), nil)
	f.docB2, _ = f.storeDoc(t, ctxFor(f.tenantB, f.subjB), nil)

	// Common-pool docs created by the admin overriding tenant_id to bootstrap.
	adminBase := ctxFor(f.tenantA, f.admin)
	f.docC, f.secC = f.storeDoc(t, adminBase, &models.BootstrapTenantID)
	f.docC2, _ = f.storeDoc(t, adminBase, &models.BootstrapTenantID)

	return f
}

// resetCommonPool clears the shared common-pool tenant's documents (sections
// cascade via the OnDelete:CASCADE FK) plus their cleanup_queue rows and
// document relation_tuples — mirroring the retention delete's child-cleanup
// order (retention.go). It intentionally writes NO deletion_events: this is
// test-fixture isolation, not a real deletion, so retention/audit counts stay
// untouched. Safe under the suite's -p 1 (no concurrent fixture).
func resetCommonPool(db *gorm.DB) error {
	const sql = `
		WITH victims AS (
			SELECT id FROM documents WHERE tenant_id = ?
		),
		purge AS (
			DELETE FROM cleanup_queue
			WHERE doc_a_id IN (SELECT id FROM victims)
			   OR doc_b_id IN (SELECT id FROM victims)
		),
		tuples AS (
			DELETE FROM relation_tuples
			WHERE object_type = ? AND object_id IN (SELECT id::text FROM victims)
		)
		DELETE FROM documents WHERE id IN (SELECT id FROM victims)`
	return db.Exec(sql, models.BootstrapTenantID, authz.TypeDocument).Error
}

func (f *authzFixture) mkTenant(t *testing.T) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "t-" + uuid.NewString()}
	require.NoError(t, f.db.Create(&ten).Error)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantSystemEdge(ten.ID)))
	return ten.ID
}

func (f *authzFixture) storeDoc(t *testing.T, ctx context.Context, override *uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	slug := "d" + uuid.NewString()
	res, err := f.svc.StoreDocument(ctx, "learnings", nil, slug, "# Title\n\n## Heading\nsome body text", true, "seed", override, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Document)
	require.NotEmpty(t, res.Document.Sections)
	return res.Document.ID, res.Document.Sections[0].ID
}

func (f *authzFixture) catSlug(t *testing.T, docID uuid.UUID) (string, string) {
	t.Helper()
	var d models.Document
	require.NoError(t, f.db.First(&d, docID).Error)
	return d.Category, d.Slug
}

// globalTupleSet snapshots every relation tuple currently stored.
func globalTupleSet(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	type row struct {
		ObjectType, ObjectID, Relation, SubjectType, SubjectID, SubjectRelation string
	}
	var rows []row
	require.NoError(t, db.Table("relation_tuples").Find(&rows).Error)
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[r.ObjectType+"|"+r.ObjectID+"|"+r.Relation+"|"+r.SubjectType+"|"+r.SubjectID+"|"+r.SubjectRelation] = true
	}
	return set
}

// TestAuthzOwnTenantAccess: a member reads and writes its own tenant's data.
func TestAuthzOwnTenantAccess(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	cat, slug := f.catSlug(t, f.docA)
	_, err := f.svc.GetDocument(ctx, cat, nil, slug, false, "", nil)
	require.NoError(t, err, "own-tenant read")

	body := "updated body"
	_, err = f.svc.UpdateSection(ctx, f.secA, &body, nil, nil)
	require.NoError(t, err, "own-tenant section write")

	require.NoError(t, f.svc.MarkVerified(ctx, f.secA, nil), "own-tenant mark_verified")

	_, err = f.svc.GetRelated(ctx, f.docA, 5, nil)
	require.NoError(t, err, "own-tenant get_related")
}

// TestAuthzCrossTenantDenied: a non-admin subject cannot reach another tenant's
// data — via the Check (get_related IDOR), via tenant scoping, or via override.
func TestAuthzCrossTenantDenied(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	// get_related on tenant B's doc: the Check on document:docB#editor denies
	// even though the target id is caller-supplied (finding #9, IDOR).
	_, err := f.svc.GetRelated(ctx, f.docB, 5, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "cross-tenant get_related must be denied")

	// mark_verified / update_section on B's section: tenant scoping denies (the
	// section is not in readTenants(A)).
	body := "x"
	_, err = f.svc.UpdateSection(ctx, f.secB, &body, nil, nil)
	require.Error(t, err, "cross-tenant update_section must be denied")
	require.ErrorIs(t, f.svc.MarkVerified(ctx, f.secB, nil), apperr.ErrNotFound)

	// Read tenant_id filter to a non-readable tenant yields not-found, never that
	// tenant's document and never an existence-revealing error (cross-tenant-reads).
	catB, slugB := f.catSlug(t, f.docB)
	_, err = f.svc.GetDocument(ctx, catB, nil, slugB, false, "", &f.tenantB)
	require.ErrorIs(t, err, apperr.ErrNotFound, "non-readable read filter must yield not-found, not a leak")
}

// TestAuthzGlobalAdminSpansTenants: a global admin overrides tenant_id, reads
// and writes across tenants, and uses admin-only tools.
func TestAuthzGlobalAdminSpansTenants(t *testing.T) {
	f := newAuthzFixture(t)
	admin := ctxFor(f.tenantA, f.admin)

	catB, slugB := f.catSlug(t, f.docB)
	_, err := f.svc.GetDocument(admin, catB, nil, slugB, false, "", &f.tenantB)
	require.NoError(t, err, "admin cross-tenant read via override")

	require.NoError(t, f.svc.MarkVerified(admin, f.secB, &f.tenantB), "admin cross-tenant mark_verified")

	_, err = f.svc.GetRelated(admin, f.docB, 5, &f.tenantB)
	require.NoError(t, err, "admin cross-tenant get_related")

	_, err = f.svc.ListTenants(admin)
	require.NoError(t, err, "admin tool")
}

// TestAuthzCommonPool: any authenticated subject reads the common pool; only an
// admin writes it (findings #8/#17 for verify/resolve).
func TestAuthzCommonPool(t *testing.T) {
	f := newAuthzFixture(t)
	user := ctxFor(f.tenantA, f.subjA)
	admin := ctxFor(f.tenantA, f.admin)

	catC, slugC := f.catSlug(t, f.docC)
	_, err := f.svc.GetDocument(user, catC, nil, slugC, false, "", nil)
	require.NoError(t, err, "any authenticated subject reads common pool")

	// Non-admin writes to the common pool are denied.
	body := "x"
	_, err = f.svc.UpdateSection(user, f.secC, &body, nil, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "non-admin common-pool update_section")
	require.ErrorIs(t, f.svc.MarkVerified(user, f.secC, nil), apperr.ErrInvalidInput, "non-admin common-pool mark_verified")

	// Admin writes to the common pool succeed.
	_, err = f.svc.UpdateSection(admin, f.secC, &body, nil, nil)
	require.NoError(t, err, "admin common-pool update_section")
	require.NoError(t, f.svc.MarkVerified(admin, f.secC, nil), "admin common-pool mark_verified")
}

// TestAuthzCleanupResolveScoping covers finding #17 for both cross-tenant and
// common-pool queue entries.
func TestAuthzCleanupResolveScoping(t *testing.T) {
	f := newAuthzFixture(t)

	// A pending entry in tenant B.
	entryB := &models.CleanupQueue{TenantID: f.tenantB, DocAID: f.docB, DocBID: f.docB2, Similarity: 0.95}
	inserted, err := repository.NewCleanupQueueRepository(f.db).Upsert(context.Background(), entryB)
	require.NoError(t, err)
	require.True(t, inserted)

	// A pending entry in the common pool.
	entryC := &models.CleanupQueue{TenantID: models.BootstrapTenantID, DocAID: f.docC, DocBID: f.docC2, Similarity: 0.95}
	inserted, err = repository.NewCleanupQueueRepository(f.db).Upsert(context.Background(), entryC)
	require.NoError(t, err)
	require.True(t, inserted)

	userA := ctxFor(f.tenantA, f.subjA)
	admin := ctxFor(f.tenantA, f.admin)

	// Cross-tenant entry: not even visible to a tenant-A caller.
	require.ErrorIs(t, f.svc.MarkCleanupDone(userA, entryB.ID, "ignored", "", nil, nil), apperr.ErrNotFound,
		"cross-tenant cleanup resolve denied")

	// Common-pool entry: visible (readTenants includes bootstrap) but the editor
	// Check denies a non-admin.
	require.ErrorIs(t, f.svc.MarkCleanupDone(userA, entryC.ID, "ignored", "", nil, nil), apperr.ErrInvalidInput,
		"non-admin common-pool cleanup resolve denied")

	// Admin can resolve the common-pool entry.
	require.NoError(t, f.svc.MarkCleanupDone(admin, entryC.ID, "ignored", "resolved by admin", nil, nil),
		"admin common-pool cleanup resolve")
}

// TestAuthzSubjectlessDenied: a request with a tenant but no resolved subject
// gets nothing beyond its own-tenant reads — every Check fails closed.
func TestAuthzSubjectlessDenied(t *testing.T) {
	f := newAuthzFixture(t)
	// Tenant in context, but NO subject attached.
	ctx := auth.WithTenantID(context.Background(), f.tenantA)

	_, err := f.svc.ListTenants(ctx)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "subjectless admin op denied")

	require.ErrorIs(t, f.svc.MarkVerified(ctx, f.secA, nil), apperr.ErrInvalidInput, "subjectless mark_verified denied")

	_, err = f.svc.GetRelated(ctx, f.docA, 5, nil)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "subjectless get_related denied")

	_, err = f.svc.GetDocument(ctx, "learnings", nil, "whatever", false, "", &f.tenantB)
	require.ErrorIs(t, err, apperr.ErrNotFound, "subjectless read filter to another tenant yields not-found")
}

// TestAuthzEscalationRegression is the crux: an admin-allowlisted Email set via
// update_tenant must grant NO admin (admin is decided by the tuple Check, not the
// mutable email). Also proves update_tenant writes ZERO authorization tuples (task 5.2).
func TestAuthzEscalationRegression(t *testing.T) {
	f := newAuthzFixture(t)
	admin := ctxFor(f.tenantA, f.admin)
	victim := ctxFor(f.tenantT, f.subjT)

	// Baseline: tenant T's subject is not an admin.
	_, err := f.svc.ListTenants(victim)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "tenant T subject must NOT be admin before")

	before := globalTupleSet(t, f.db)

	// Admin flips EVERY field of tenant T, including Email to an "admin" address.
	adminEmail := "admin-allowlisted@example.com"
	name := "renamed-" + uuid.NewString()
	mode := models.StalenessModeHard
	dg := true
	cs := true
	_, err = f.svc.UpdateTenant(admin, f.tenantT, service.UpdateTenantFields{
		Name:               &name,
		Email:              &adminEmail,
		StalenessMode:      &mode,
		DuplicateGuard:     &dg,
		CleanupScanEnabled: &cs,
	})
	require.NoError(t, err)

	// 5.2: no authorization tuple was written by editing the tenant.
	after := globalTupleSet(t, f.db)
	require.Equal(t, before, after, "update_tenant must not write or remove any relation tuple")

	// The bug we are killing: the mutated email must NOT confer admin.
	_, err = f.svc.ListTenants(victim)
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "escalation via tenant.Email must be dead")

	_, err = f.svc.CreateTenant(victim, "x-"+uuid.NewString(), "")
	require.ErrorIs(t, err, apperr.ErrInvalidInput, "escalation via tenant.Email must be dead (create_tenant)")
}
