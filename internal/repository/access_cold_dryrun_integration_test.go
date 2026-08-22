//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// coldCandidatePaths runs the access-cold dry-run and returns the set of
// candidate document paths it reports.
func coldCandidatePaths(t *testing.T, db *gorm.DB, tenantID uuid.UUID, cutoffs map[string]time.Time) map[string]bool {
	t.Helper()
	findings, err := repository.NewLintRepository(db).CheckAccessCold(context.Background(), tenantID, cutoffs)
	if err != nil {
		t.Fatalf("CheckAccessCold: %v", err)
	}
	paths := make(map[string]bool, len(findings))
	for _, f := range findings {
		if f.Check != "access_cold" {
			t.Errorf("finding.Check = %q, want access_cold", f.Check)
		}
		paths[f.DocumentPath] = true
	}
	return paths
}

func learningPath(slug string) string { return models.BuildPath("learnings", nil, slug) }

// TestCheckAccessCold_DryRun (5.2) proves the lint dry-run lists exactly the
// cold, unpinned, non-episodic docs for the caller's tenant (pinned/episodic/
// recent/other-tenant absent) and changes no document state (read-only).
func TestCheckAccessCold_DryRun(t *testing.T) {
	db := openRetentionPG(t)
	tenantA := retTenant(t, db)
	tenantB := retTenant(t, db)

	now := time.Now()
	cutoffs := map[string]time.Time{models.DocTypeLearning: now.Add(-30 * 24 * time.Hour)}
	cold := now.Add(-40 * 24 * time.Hour)       // past cutoff
	justInside := now.Add(-20 * 24 * time.Hour) // newer than cutoff

	coldSlug := "cold-" + uuid.NewString()
	coldDoc := accessDoc(t, db, tenantA, models.DocTypeLearning, coldSlug, &cold, now, false, nil)
	recentDoc := accessDoc(t, db, tenantA, models.DocTypeLearning, "warm-"+uuid.NewString(), &justInside, now, false, nil)
	neverInside := accessDoc(t, db, tenantA, models.DocTypeLearning, "ncr-"+uuid.NewString(), nil, justInside, false, nil)
	pinnedCold := accessDoc(t, db, tenantA, models.DocTypeLearning, "pin-"+uuid.NewString(), &cold, now, true, nil)
	episodicCold := accessDoc(t, db, tenantA, models.DocTypeJournal, "jrn-"+uuid.NewString(), &cold, now, false, nil)
	otherCold := accessDoc(t, db, tenantB, models.DocTypeLearning, "other-"+uuid.NewString(), &cold, now, false, nil)

	paths := coldCandidatePaths(t, db, tenantA, cutoffs)
	if len(paths) != 1 || !paths[learningPath(coldSlug)] {
		t.Fatalf("dry-run candidates = %v, want only %s", paths, learningPath(coldSlug))
	}

	// Read-only: nothing was archived or deleted.
	for _, id := range []uuid.UUID{coldDoc, recentDoc, neverInside, pinnedCold, episodicCold, otherCold} {
		if docArchived(t, db, id) {
			t.Errorf("doc %s archived by the dry-run; it must change nothing", id)
		}
		if !docExists(t, db, id) {
			t.Errorf("doc %s deleted by the dry-run; it must change nothing", id)
		}
	}
}

// TestCheckAccessCold_FalseEvictionSafety (6.2) asserts via CheckAccessCold and
// ArchiveAccessCold that only a cold unpinned non-episodic doc is a candidate,
// and a freshly-backfilled corpus (last_accessed_at=now) has ZERO candidates (D6).
func TestCheckAccessCold_FalseEvictionSafety(t *testing.T) {
	db := openRetentionPG(t)
	ctx := context.Background()
	retRepo := repository.NewRetentionRepository(db)

	now := time.Now()
	cutoffs := map[string]time.Time{models.DocTypeLearning: now.Add(-30 * 24 * time.Hour)}
	cold := now.Add(-40 * 24 * time.Hour)
	justInside := now.Add(-29 * 24 * time.Hour) // just inside the window -> survives

	tenant := retTenant(t, db)
	coldSlug := "cold-" + uuid.NewString()
	coldDoc := accessDoc(t, db, tenant, models.DocTypeLearning, coldSlug, &cold, now, false, nil)
	justInsideDoc := accessDoc(t, db, tenant, models.DocTypeLearning, "edge-"+uuid.NewString(), &justInside, cold, false, nil)
	neverInsideDoc := accessDoc(t, db, tenant, models.DocTypeLearning, "ncr-"+uuid.NewString(), nil, justInside, false, nil)
	pinnedColdDoc := accessDoc(t, db, tenant, models.DocTypeLearning, "pin-"+uuid.NewString(), &cold, now, true, nil)
	episodicColdDoc := accessDoc(t, db, tenant, models.DocTypeJournal, "jrn-"+uuid.NewString(), &cold, now, false, nil)

	// Dry-run: only the cold candidate is listed.
	paths := coldCandidatePaths(t, db, tenant, cutoffs)
	if len(paths) != 1 || !paths[learningPath(coldSlug)] {
		t.Fatalf("candidates = %v, want only %s", paths, learningPath(coldSlug))
	}

	// Eviction agrees with the dry-run: exactly the cold candidate is archived.
	n, err := retRepo.ArchiveAccessCold(ctx, tenant, cutoffs)
	if err != nil {
		t.Fatalf("ArchiveAccessCold: %v", err)
	}
	if n != 1 {
		t.Fatalf("ArchiveAccessCold rows = %d, want 1 (only the cold candidate)", n)
	}
	if !docArchived(t, db, coldDoc) {
		t.Error("cold candidate should have been archived")
	}
	for name, id := range map[string]uuid.UUID{
		"just-inside":    justInsideDoc,
		"never-accessed": neverInsideDoc,
		"pinned-cold":    pinnedColdDoc,
		"episodic-cold":  episodicColdDoc,
	} {
		if docArchived(t, db, id) {
			t.Errorf("%s doc must survive eviction", name)
		}
	}

	// D6: a freshly-backfilled corpus (all last_accessed_at = now) has ZERO
	// candidates and archives nothing immediately after opt-in.
	fresh := retTenant(t, db)
	for i := 0; i < 5; i++ {
		accessDoc(t, db, fresh, models.DocTypeLearning, "fresh-"+uuid.NewString(), &now, cold, false, nil)
	}
	if paths := coldCandidatePaths(t, db, fresh, cutoffs); len(paths) != 0 {
		t.Fatalf("freshly-backfilled corpus has %d candidates, want 0 (D6 instant-eviction safeguard)", len(paths))
	}
	nFresh, err := retRepo.ArchiveAccessCold(ctx, fresh, cutoffs)
	if err != nil {
		t.Fatalf("ArchiveAccessCold(fresh): %v", err)
	}
	if nFresh != 0 {
		t.Fatalf("freshly-backfilled corpus archived %d docs, want 0 (D6)", nFresh)
	}
}
