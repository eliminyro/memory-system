//go:build integration

package repository_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/database"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// dupTestDim matches the dimension the other Postgres integration tests use so a
// shared test DB migrated by one suite doesn't trip the other's dimension guard.
const dupTestDim = 768

func openLintPG(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; Postgres integration test skipped")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.Migrate(db, "fake", "fake", dupTestDim, database.TenantColumnDefaults{StalenessMode: "off"}, database.BaselineGlobalConfigDefaults()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func normalizeVec(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return
	}
	for i := range v {
		v[i] /= n
	}
}

// randUnit returns a random unit vector. In 768 dims two independent draws have
// cosine near zero (std ~1/sqrt(768)), so noise stays far below any threshold.
func randUnit(rng *rand.Rand) []float32 {
	v := make([]float32, dupTestDim)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	normalizeVec(v)
	return v
}

// nearDup returns a vector that is cosine ~0.999 to base.
func nearDup(base []float32, rng *rand.Rand, eps float32) []float32 {
	v := make([]float32, len(base))
	for i := range base {
		v[i] = base[i] + eps*float32(rng.NormFloat64())
	}
	normalizeVec(v)
	return v
}

func seedTenant(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	tn := &models.Tenant{
		ID:                 uuid.New(),
		Name:               "lint-dup-" + uuid.NewString(),
		CleanupScanEnabled: true,
		StalenessMode:      models.StalenessModeOff,
	}
	if err := db.Create(tn).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	return tn.ID
}

func cleanupTenant(db *gorm.DB, tenantID uuid.UUID) {
	// Deleting the documents cascades their sections via the FK constraint.
	db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenantID)
	db.Exec("DELETE FROM tenants WHERE id = ?", tenantID)
}

func seedDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, slug string, emb []float32) uuid.UUID {
	t.Helper()
	doc := &models.Document{
		ID:       uuid.New(),
		TenantID: tenantID,
		Category: "learnings",
		Slug:     slug,
		Title:    slug,
		DocType:  "learning",
	}
	if err := db.Create(doc).Error; err != nil {
		t.Fatalf("create doc %s: %v", slug, err)
	}
	sec := &models.Section{
		DocumentID: doc.ID,
		Ordinal:    0,
		Content:    slug,
		Embedding:  pgvector.NewVector(emb),
	}
	if err := db.Create(sec).Error; err != nil {
		t.Fatalf("create section %s: %v", slug, err)
	}
	return doc.ID
}

func seedNoise(t *testing.T, db *gorm.DB, tenantID uuid.UUID, n int, rng *rand.Rand) {
	t.Helper()
	docs := make([]models.Document, n)
	secs := make([]models.Section, n)
	for i := 0; i < n; i++ {
		id := uuid.New()
		docs[i] = models.Document{
			ID:       id,
			TenantID: tenantID,
			Category: "learnings",
			Slug:     fmt.Sprintf("noise-%d", i),
			Title:    "noise",
			DocType:  "learning",
		}
		secs[i] = models.Section{
			DocumentID: id,
			Ordinal:    0,
			Content:    fmt.Sprintf("noise %d", i),
			Embedding:  pgvector.NewVector(randUnit(rng)),
		}
	}
	if err := db.CreateInBatches(docs, 100).Error; err != nil {
		t.Fatalf("seed noise docs: %v", err)
	}
	if err := db.CreateInBatches(secs, 100).Error; err != nil {
		t.Fatalf("seed noise sections: %v", err)
	}
}

// TestFindNearDuplicatePairs_Integration seeds a larger section set and asserts the
// bounded nightly scan (a) finishes within its timeout, (b) still surfaces a planted
// near-duplicate, and (c) never returns a common-pool pair (audit #14 — tenant scope).
func TestFindNearDuplicatePairs_Integration(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(1))

	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	// A larger set of unrelated sections — the O(N^2) blast radius the bound protects.
	seedNoise(t, db, tenantID, 300, rng)

	// Planted near-duplicate pair within the tenant.
	base := randUnit(rng)
	a := seedDoc(t, db, tenantID, "planted-a", base)
	b := seedDoc(t, db, tenantID, "planted-b", nearDup(base, rng, 0.001))

	// A near-identical doc in the shared common (bootstrap) pool. The old
	// readTenants-merged scope would have paired it with tenant-x; tenant-owned
	// scope must exclude it.
	xEmb := randUnit(rng)
	seedDoc(t, db, tenantID, "tenant-x", xEmb)
	commonC := seedDoc(t, db, models.BootstrapTenantID, "common-c-"+uuid.NewString(), nearDup(xEmb, rng, 0.001))
	t.Cleanup(func() { db.Exec("DELETE FROM documents WHERE id = ?", commonC) })

	lr := repository.NewLintRepository(db)

	start := time.Now()
	pairs, err := lr.FindNearDuplicatePairs(context.Background(), tenantID, models.ScanThreshold)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// The bound is a 30s statement_timeout; a healthy run here is sub-second.
	// Anything near the timeout means the bound was the only thing that stopped it.
	if elapsed > 25*time.Second {
		t.Fatalf("scan did not complete within the bound: took %s", elapsed)
	}

	foundPlant := false
	for _, p := range pairs {
		if (p.DocAID == a && p.DocBID == b) || (p.DocAID == b && p.DocBID == a) {
			foundPlant = true
			if p.Similarity < 0.95 {
				t.Errorf("planted pair similarity %.4f < 0.95", p.Similarity)
			}
		}
		if p.DocAID == commonC || p.DocBID == commonC {
			t.Errorf("common-pool doc leaked into per-tenant scan: %+v", p)
		}
	}
	if !foundPlant {
		t.Fatalf("planted near-duplicate pair not surfaced among %d returned pairs", len(pairs))
	}
}

// TestCheckNearDuplicates_PairCap_Integration proves the user-triggerable lint (audit
// #10) enforces its result-set cap: a generous cap surfaces all pairs, cap=1 truncates.
func TestCheckNearDuplicates_PairCap_Integration(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(2))

	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	// Three independent planted near-duplicate pairs (six docs).
	for i := 0; i < 3; i++ {
		base := randUnit(rng)
		seedDoc(t, db, tenantID, fmt.Sprintf("dupA-%d", i), base)
		seedDoc(t, db, tenantID, fmt.Sprintf("dupB-%d", i), nearDup(base, rng, 0.001))
	}

	lr := repository.NewLintRepository(db)
	th := repository.DefaultLintThresholds()
	th.DuplicateSimilarity = 0.90

	th.DuplicateMaxPairs = 100
	all, err := lr.CheckNearDuplicates(context.Background(), tenantID, th)
	if err != nil {
		t.Fatalf("check (large cap): %v", err)
	}
	if len(all) < 3 {
		t.Fatalf("expected >=3 near-duplicate findings with a generous cap, got %d", len(all))
	}

	th.DuplicateMaxPairs = 1
	capped, err := lr.CheckNearDuplicates(context.Background(), tenantID, th)
	if err != nil {
		t.Fatalf("check (cap=1): %v", err)
	}
	if len(capped) > 1 {
		t.Fatalf("pair cap not enforced: got %d findings, want <=1", len(capped))
	}
	if len(capped) == 0 {
		t.Fatalf("cap=1 returned no findings; recall is broken")
	}
}
