//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// seedRetDoc creates a one-section document of docType under tenantID and returns
// its id, so the retention tests can backdate / pin / access it individually.
func seedRetDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, slug, docType string, rng *rand.Rand) uuid.UUID {
	t.Helper()
	doc := &models.Document{
		ID: uuid.New(), TenantID: tenantID, Category: "learnings",
		Slug: slug, Title: slug, DocType: docType,
	}
	require.NoError(t, db.Create(doc).Error)
	require.NoError(t, db.Create(&models.Section{
		DocumentID: doc.ID, Ordinal: 0, Content: slug,
		Embedding: pgvector.NewVector(randUnit(rng)),
	}).Error)
	return doc.ID
}

// coldenDoc backdates a doc and its sections by `days` so its creation sits `days`
// in the past (the sole retention clock now — no access/verify reprieve).
func coldenDoc(t *testing.T, db *gorm.DB, docID uuid.UUID, days int) {
	t.Helper()
	require.NoError(t, db.Exec(
		`UPDATE documents SET created_at = NOW() - make_interval(days => ?), last_accessed_at = NULL WHERE id = ?`,
		days, docID).Error)
	require.NoError(t, db.Exec(
		`UPDATE sections SET created_at = NOW() - make_interval(days => ?), verified_at = NULL WHERE document_id = ?`,
		days, docID).Error)
}

func docExists(t *testing.T, db *gorm.DB, id uuid.UUID) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&models.Document{}).Where("id = ?", id).Count(&n).Error)
	return n > 0
}

// TestRetentionCandidates_Selection covers the created_at predicate: no access or
// verify reprieve, plus the pinned / expiration-disabled / long-window exclusions.
func TestRetentionCandidates_Selection(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(7))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })
	ctx := context.Background()

	cutoffs := map[string]int{
		models.DocTypeLearning:  50,  // short window
		models.DocTypeReference: 300, // long window
	}

	cold := seedRetDoc(t, db, tenantID, "cold", models.DocTypeLearning, rng)
	coldenDoc(t, db, cold, 100)

	accessed := seedRetDoc(t, db, tenantID, "accessed", models.DocTypeLearning, rng)
	coldenDoc(t, db, accessed, 100)
	require.NoError(t, db.Exec(`UPDATE documents SET last_accessed_at = NOW() WHERE id = ?`, accessed).Error)

	verified := seedRetDoc(t, db, tenantID, "verified", models.DocTypeLearning, rng)
	coldenDoc(t, db, verified, 100)
	require.NoError(t, db.Exec(`UPDATE sections SET verified_at = NOW() WHERE document_id = ?`, verified).Error)

	pinned := seedRetDoc(t, db, tenantID, "pinned", models.DocTypeLearning, rng)
	coldenDoc(t, db, pinned, 100)
	require.NoError(t, db.Exec(`UPDATE documents SET pinned = true WHERE id = ?`, pinned).Error)

	// doc_type absent from cutoffs (expiration disabled) is never a candidate.
	pref := seedRetDoc(t, db, tenantID, "pref", models.DocTypePreference, rng)
	coldenDoc(t, db, pref, 100)

	// long-window doc_type, cold-aged but still inside its 300-day window.
	ref := seedRetDoc(t, db, tenantID, "ref", models.DocTypeReference, rng)
	coldenDoc(t, db, ref, 100)

	got, err := repository.NewRetentionRepository(db).Candidates(ctx, tenantID, cutoffs)
	require.NoError(t, err)

	ids := map[uuid.UUID]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	require.True(t, ids[cold], "doc created past its window should be a candidate")
	require.True(t, ids[accessed], "recent access no longer reprieves a perishable")
	require.True(t, ids[verified], "recent re-verification no longer reprieves a perishable")
	require.False(t, ids[pinned], "pinned doc must be excluded")
	require.False(t, ids[pref], "expiration-disabled doc_type must be excluded")
	require.False(t, ids[ref], "doc within its long window must be excluded")
}

// TestDeleteExpiredCold_PurgesAndAudits covers task 2.2: the purge cascade, the
// FK edge cascade, the audit row, and that non-candidates are untouched.
func TestDeleteExpiredCold_PurgesAndAudits(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(11))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })
	t.Cleanup(func() { db.Exec(`DELETE FROM deletion_events WHERE tenant_id = ?`, tenantID) })
	ctx := context.Background()

	cutoffs := map[string]int{models.DocTypeLearning: 50}

	cold := seedRetDoc(t, db, tenantID, "evictme", models.DocTypeLearning, rng)
	coldenDoc(t, db, cold, 100)

	// a live partner plus an edge from the candidate proves the FK edge cascade.
	partner := seedRetDoc(t, db, tenantID, "partner", models.DocTypeLearning, rng)
	require.NoError(t, db.Create(&models.Edge{
		TenantID: tenantID, SourceDocumentID: cold, TargetDocumentID: partner,
		EdgeType: models.EdgeRelatesTo,
	}).Error)

	fresh := seedRetDoc(t, db, tenantID, "fresh", models.DocTypeLearning, rng)

	deleted, err := repository.NewRetentionRepository(db).DeleteExpiredCold(ctx, tenantID, cutoffs)
	require.NoError(t, err)
	require.Len(t, deleted, 1)
	require.Equal(t, cold, deleted[0].ID)
	require.Equal(t, models.DocTypeLearning, deleted[0].DocType)

	require.False(t, docExists(t, db, cold), "candidate document must be purged")

	var secCount int64
	require.NoError(t, db.Model(&models.Section{}).Where("document_id = ?", cold).Count(&secCount).Error)
	require.Zero(t, secCount, "candidate sections must be purged")

	var edgeCount int64
	require.NoError(t, db.Model(&models.Edge{}).Where("source_document_id = ?", cold).Count(&edgeCount).Error)
	require.Zero(t, edgeCount, "candidate edges must cascade away")

	var ev models.DeletionEvent
	require.NoError(t, db.Where("tenant_id = ? AND reason = ?", tenantID, models.DeletionReasonRetention).First(&ev).Error)
	require.Equal(t, models.DocTypeLearning, ev.DocType)
	require.Equal(t, models.BuildPath("learnings", nil, "evictme"), ev.DocumentPath)

	require.True(t, docExists(t, db, fresh), "fresh non-candidate must survive")
	require.True(t, docExists(t, db, partner), "edge partner must survive")
}

// TestRetentionCandidateFindings_DryRun covers task 3.3 at the repository level:
// the dry-run lists candidates, is toggle-independent, and mutates nothing.
func TestRetentionCandidateFindings_DryRun(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(13))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })
	ctx := context.Background()

	cutoffs := map[string]int{models.DocTypeLearning: 50}
	cold := seedRetDoc(t, db, tenantID, "wouldevict", models.DocTypeLearning, rng)
	coldenDoc(t, db, cold, 100)

	findings, err := repository.NewRetentionRepository(db).CandidateFindings(ctx, tenantID, cutoffs)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "retention_candidate", findings[0].Check)
	require.Equal(t, models.BuildPath("learnings", nil, "wouldevict"), findings[0].DocumentPath)

	require.True(t, docExists(t, db, cold), "dry-run must not delete anything")
}

// TestPerishableRetention_MigratedDefaults covers task 6.2: Migrate flips the
// prunable/expiration defaults, drops the grace column, and the resulting cutoffs
// evict an old journal while sparing knowledge of any age (and pinned journals).
func TestPerishableRetention_MigratedDefaults(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(23))
	ctx := context.Background()

	get := func(dt string) models.DocTypePolicy {
		var row models.DocTypePolicy
		require.NoError(t, db.Where("doc_type = ?", dt).First(&row).Error)
		return row
	}
	ref := get(models.DocTypeReference)
	require.NotNil(t, ref.Prunable)
	require.False(t, *ref.Prunable, "reference must be non-prunable after migrate")

	jr := get(models.DocTypeJournal)
	require.NotNil(t, jr.Prunable)
	require.True(t, *jr.Prunable)
	require.NotNil(t, jr.ExpirationAgeDays)
	require.Equal(t, 30, *jr.ExpirationAgeDays)

	hf := get(models.DocTypeHandoff)
	require.NotNil(t, hf.Prunable)
	require.True(t, *hf.Prunable)
	require.NotNil(t, hf.ExpirationAgeDays)
	require.Equal(t, 90, *hf.ExpirationAgeDays)

	// The now-dead grace column must be gone.
	var graceCols int64
	require.NoError(t, db.Raw(
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'instance_config' AND column_name = 'retention_grace_days'`,
	).Scan(&graceCols).Error)
	require.Zero(t, graceCols, "retention_grace_days column must be dropped")

	var rows []models.DocTypePolicy
	require.NoError(t, db.Find(&rows).Error)
	eff, err := models.ResolveDocTypePolicies(rows)
	require.NoError(t, err)
	cutoffs := repository.BuildRetentionCutoffs(eff)
	require.Equal(t, 30, cutoffs[models.DocTypeJournal])
	require.Equal(t, 90, cutoffs[models.DocTypeHandoff])
	_, refIn := cutoffs[models.DocTypeReference]
	require.False(t, refIn, "non-prunable reference must not be a retention target")

	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	oldJournal := seedRetDoc(t, db, tenantID, "2020-01-01", models.DocTypeJournal, rng)
	coldenDoc(t, db, oldJournal, 40)

	pinnedJournal := seedRetDoc(t, db, tenantID, "2020-02-02", models.DocTypeJournal, rng)
	coldenDoc(t, db, pinnedJournal, 40)
	require.NoError(t, db.Exec(`UPDATE documents SET pinned = true WHERE id = ?`, pinnedJournal).Error)

	oldKnowledge := seedRetDoc(t, db, tenantID, "ancient", models.DocTypeLearning, rng)
	coldenDoc(t, db, oldKnowledge, 900)

	got, err := repository.NewRetentionRepository(db).Candidates(ctx, tenantID, cutoffs)
	require.NoError(t, err)
	ids := map[uuid.UUID]bool{}
	for _, c := range got {
		ids[c.ID] = true
	}
	require.True(t, ids[oldJournal], "journal older than 30d is a candidate")
	require.False(t, ids[pinnedJournal], "pinned journal is never a candidate")
	require.False(t, ids[oldKnowledge], "knowledge is never a candidate at any age")
}
