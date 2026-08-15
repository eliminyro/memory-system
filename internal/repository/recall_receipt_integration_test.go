//go:build integration

package repository_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

const recallDim = 768

func openRecallPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", recallDim, database.TenantColumnDefaults{StalenessMode: "off"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// recallTenant creates a tenant leaving Type at its Go zero value (""), so the
// column's NOT NULL DEFAULT 'shared' backfills it — i.e. this is a SHARED tenant.
func recallTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "recall-" + uuid.NewString()}
	if err := db.Create(&ten).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return ten.ID
}

// recallTenantOfType creates a tenant with an explicit Type (personal or
// shared), for tests that must control tenant type precisely.
func recallTenantOfType(t *testing.T, db *gorm.DB, tenantType string) uuid.UUID {
	t.Helper()
	ten := models.Tenant{ID: uuid.New(), Name: "recall-" + uuid.NewString(), Type: tenantType}
	if err := db.Create(&ten).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return ten.ID
}

// recallSection inserts a document + one section under tenantID and returns
// the section id.
func recallSection(t *testing.T, db *gorm.DB, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	doc := models.Document{
		ID:       uuid.New(),
		TenantID: tenantID,
		Category: "learnings",
		Slug:     "recall-" + uuid.NewString(),
		Title:    "T",
		DocType:  models.DocTypeLearning,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	sec := models.Section{
		ID:         uuid.New(),
		DocumentID: doc.ID,
		Content:    "body",
		Embedding:  pgvector.NewVector(make([]float32, recallDim)),
	}
	if err := db.Create(&sec).Error; err != nil {
		t.Fatalf("create section: %v", err)
	}
	return sec.ID
}

func recallSectionCounts(t *testing.T, db *gorm.DB, id uuid.UUID) (hit, miss int) {
	t.Helper()
	var s models.Section
	if err := db.Where("id = ?", id).First(&s).Error; err != nil {
		t.Fatalf("load section: %v", err)
	}
	return s.HitCount, s.MissCount
}

// TestRecallReceipt_CreateGetByID_TenantScoped proves Create/GetByID round-trip
// the section id array and that GetByID is tenant-scoped: a cross-tenant lookup
// is ErrNotFound, never the other tenant's receipt.
func TestRecallReceipt_CreateGetByID_TenantScoped(t *testing.T) {
	db := openRecallPG(t)
	ctx := context.Background()
	repo := repository.NewRecallReceiptRepository(db)

	tenantA := recallTenant(t, db)
	tenantB := recallTenant(t, db)
	sec1 := recallSection(t, db, tenantA)
	sec2 := recallSection(t, db, tenantA)

	recallID, err := repo.Create(ctx, tenantA, []uuid.UUID{sec1, sec2})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if recallID == uuid.Nil {
		t.Fatal("expected a non-nil recall id")
	}

	got, err := repo.GetByID(ctx, tenantA, recallID)
	if err != nil {
		t.Fatalf("get by id (own tenant): %v", err)
	}
	if len(got.SectionIDs) != 2 {
		t.Fatalf("SectionIDs = %v, want 2 entries", got.SectionIDs)
	}
	if got.ReportedAt != nil {
		t.Error("fresh receipt must not be reported yet")
	}

	if _, err := repo.GetByID(ctx, tenantB, recallID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-tenant GetByID error = %v, want ErrNotFound", err)
	}
}

// TestRecallReceipt_ReportOutcome_HitAndMiss proves ReportOutcome stamps
// ReportedAt and increments hit_count/miss_count on exactly the named
// sections, leaving an unrelated section's counts at zero.
func TestRecallReceipt_ReportOutcome_HitAndMiss(t *testing.T) {
	db := openRecallPG(t)
	ctx := context.Background()
	repo := repository.NewRecallReceiptRepository(db)

	tenant := recallTenant(t, db)
	hitSec := recallSection(t, db, tenant)
	missSec := recallSection(t, db, tenant)
	untouched := recallSection(t, db, tenant)

	hitRecall, err := repo.Create(ctx, tenant, []uuid.UUID{hitSec})
	if err != nil {
		t.Fatalf("create hit receipt: %v", err)
	}
	if err := repo.ReportOutcome(ctx, tenant, hitRecall, models.RecallOutcomeSuccess); err != nil {
		t.Fatalf("report success: %v", err)
	}

	missRecall, err := repo.Create(ctx, tenant, []uuid.UUID{missSec})
	if err != nil {
		t.Fatalf("create miss receipt: %v", err)
	}
	if err := repo.ReportOutcome(ctx, tenant, missRecall, models.RecallOutcomeFailure); err != nil {
		t.Fatalf("report failure: %v", err)
	}

	if hit, miss := recallSectionCounts(t, db, hitSec); hit != 1 || miss != 0 {
		t.Errorf("hit section counts = (%d,%d), want (1,0)", hit, miss)
	}
	if hit, miss := recallSectionCounts(t, db, missSec); hit != 0 || miss != 1 {
		t.Errorf("miss section counts = (%d,%d), want (0,1)", hit, miss)
	}
	if hit, miss := recallSectionCounts(t, db, untouched); hit != 0 || miss != 0 {
		t.Errorf("untouched section counts = (%d,%d), want (0,0)", hit, miss)
	}

	reReceipt, err := repo.GetByID(ctx, tenant, hitRecall)
	if err != nil {
		t.Fatalf("reload hit receipt: %v", err)
	}
	if reReceipt.ReportedAt == nil {
		t.Error("ReportOutcome must stamp ReportedAt")
	}
}

// TestRecallReceipt_ReportOutcome_ScopedByTenantType proves crediting only
// touches sections owned by the receipt's own tenant or any SHARED-type
// tenant — NEVER another PERSONAL tenant. This is the Phase B cross-tenant
// manipulation guard (user ruling): a served section from an unrelated
// personal tenant must never be credited just by appearing in a search result.
func TestRecallReceipt_ReportOutcome_ScopedByTenantType(t *testing.T) {
	db := openRecallPG(t)
	ctx := context.Background()
	repo := repository.NewRecallReceiptRepository(db)

	home := recallTenantOfType(t, db, models.TenantTypePersonal)
	otherShared := recallTenantOfType(t, db, models.TenantTypeShared)
	otherPersonal := recallTenantOfType(t, db, models.TenantTypePersonal)

	homeSec := recallSection(t, db, home)
	sharedSec := recallSection(t, db, otherShared)
	personalSec := recallSection(t, db, otherPersonal)

	recallID, err := repo.Create(ctx, home, []uuid.UUID{homeSec, sharedSec, personalSec})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.ReportOutcome(ctx, home, recallID, models.RecallOutcomeSuccess); err != nil {
		t.Fatalf("report outcome: %v", err)
	}

	if hit, _ := recallSectionCounts(t, db, homeSec); hit != 1 {
		t.Errorf("home-tenant section hit = %d, want 1", hit)
	}
	if hit, _ := recallSectionCounts(t, db, sharedSec); hit != 1 {
		t.Errorf("other-shared-tenant section hit = %d, want 1 (shared tenants are creditable)", hit)
	}
	if hit, _ := recallSectionCounts(t, db, personalSec); hit != 0 {
		t.Errorf("other-personal-tenant section hit = %d, want 0 (must NEVER be credited)", hit)
	}
}

// TestRecallReceipt_ReportOutcome_ConcurrentDuplicateCreditsOnce proves N
// concurrent ReportOutcome calls for the same recall_id credit exactly
// once: the atomic "reported_at IS NULL" guard, combined with Postgres row
// locking, means only the goroutine that actually flips reported_at proceeds
// to credit — closing the GetByID-then-check TOCTOU a separate read-then-write
// would have.
func TestRecallReceipt_ReportOutcome_ConcurrentDuplicateCreditsOnce(t *testing.T) {
	db := openRecallPG(t)
	ctx := context.Background()
	repo := repository.NewRecallReceiptRepository(db)

	tenant := recallTenant(t, db)
	sec := recallSection(t, db, tenant)
	recallID, err := repo.Create(ctx, tenant, []uuid.UUID{sec})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = repo.ReportOutcome(ctx, tenant, recallID, models.RecallOutcomeSuccess)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	hit, miss := recallSectionCounts(t, db, sec)
	if hit != 1 {
		t.Errorf("hit count = %d, want exactly 1 despite %d concurrent reports", hit, n)
	}
	if miss != 0 {
		t.Errorf("miss count = %d, want 0", miss)
	}
}

// TestRecallReceipt_PruneExpired proves pruning removes only receipts older
// than the cutoff and never touches hit/miss counts already credited on
// sections — counts live independently of receipt lifetime (design D3).
func TestRecallReceipt_PruneExpired(t *testing.T) {
	db := openRecallPG(t)
	ctx := context.Background()
	repo := repository.NewRecallReceiptRepository(db)

	tenant := recallTenant(t, db)
	sec := recallSection(t, db, tenant)

	oldRecall, err := repo.Create(ctx, tenant, []uuid.UUID{sec})
	if err != nil {
		t.Fatalf("create old receipt: %v", err)
	}
	freshRecall, err := repo.Create(ctx, tenant, []uuid.UUID{sec})
	if err != nil {
		t.Fatalf("create fresh receipt: %v", err)
	}

	// Credit the old receipt BEFORE backdating it, so the prune's (expected
	// no-op) effect on counts is actually exercised.
	if err := repo.ReportOutcome(ctx, tenant, oldRecall, models.RecallOutcomeSuccess); err != nil {
		t.Fatalf("report old receipt: %v", err)
	}

	old := time.Now().Add(-100 * time.Hour)
	if err := db.Exec(`UPDATE recall_receipts SET created_at = ? WHERE recall_id = ?`, old, oldRecall).Error; err != nil {
		t.Fatalf("backdate old receipt: %v", err)
	}

	n, err := repo.PruneExpired(ctx, time.Now().Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}

	if _, err := repo.GetByID(ctx, tenant, oldRecall); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("old receipt should be pruned, GetByID error = %v", err)
	}
	if _, err := repo.GetByID(ctx, tenant, freshRecall); err != nil {
		t.Errorf("fresh receipt should survive, got error: %v", err)
	}
	if hit, miss := recallSectionCounts(t, db, sec); hit != 1 || miss != 0 {
		t.Errorf("counts after prune = (%d,%d), want (1,0) — prune must not touch counts", hit, miss)
	}
}
