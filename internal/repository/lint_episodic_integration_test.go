//go:build integration

package repository_test

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// seedTypedDoc is seedDoc but with an explicit category/doc_type, for episodic
// (journal) vs curated (learning) comparisons in the exemption tests below.
func seedTypedDoc(t *testing.T, db *gorm.DB, tenantID uuid.UUID, category, docType, slug string, emb []float32) uuid.UUID {
	t.Helper()
	doc := &models.Document{
		ID:       uuid.New(),
		TenantID: tenantID,
		Category: category,
		Slug:     slug,
		Title:    slug,
		DocType:  docType,
	}
	require.NoError(t, db.Create(doc).Error)
	require.NoError(t, db.Create(&models.Section{
		DocumentID: doc.ID, Ordinal: 0, Content: slug, Embedding: pgvector.NewVector(emb),
	}).Error)
	return doc.ID
}

// TestCheckStale_ExcludesEpisodic proves journal docs never surface in the
// stale lint finding, while a curated doc of the same age still does.
func TestCheckStale_ExcludesEpisodic(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(21))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	journalID := seedTypedDoc(t, db, tenantID, "journal", models.DocTypeJournal, "je-"+uuid.NewString(), randUnit(rng))
	learningID := seedTypedDoc(t, db, tenantID, "learnings", models.DocTypeLearning, "le-"+uuid.NewString(), randUnit(rng))
	require.NoError(t, db.Exec(
		`UPDATE documents SET updated_at = NOW() - INTERVAL '400 days' WHERE id IN (?, ?)`, journalID, learningID,
	).Error)

	lr := repository.NewLintRepository(db)
	th := repository.DefaultLintThresholds()
	findings, err := lr.CheckStale(context.Background(), tenantID, th)
	require.NoError(t, err)

	var journalDoc, learningDoc models.Document
	require.NoError(t, db.First(&journalDoc, journalID).Error)
	require.NoError(t, db.First(&learningDoc, learningID).Error)
	journalPath := journalDoc.Path()
	learningPath := learningDoc.Path()

	var sawJournal, sawLearning bool
	for _, f := range findings {
		if f.DocumentPath == journalPath {
			sawJournal = true
		}
		if f.DocumentPath == learningPath {
			sawLearning = true
		}
	}
	require.False(t, sawJournal, "episodic (journal) doc must be excluded from the stale finding")
	require.True(t, sawLearning, "curated doc of the same age must still be flagged stale")
}

// TestFindNearDuplicatePairs_ExcludesEpisodic proves a near-duplicate pair of
// journal docs never enters the cleanup-scan candidate set, while a curated
// near-duplicate pair still does.
func TestFindNearDuplicatePairs_ExcludesEpisodic(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(22))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	jBase := randUnit(rng)
	jA := seedTypedDoc(t, db, tenantID, "journal", models.DocTypeJournal, "ja-"+uuid.NewString(), jBase)
	jB := seedTypedDoc(t, db, tenantID, "journal", models.DocTypeJournal, "jb-"+uuid.NewString(), nearDup(jBase, rng, 0.001))

	lBase := randUnit(rng)
	lA := seedDoc(t, db, tenantID, "la-"+uuid.NewString(), lBase)
	lB := seedDoc(t, db, tenantID, "lb-"+uuid.NewString(), nearDup(lBase, rng, 0.001))

	lr := repository.NewLintRepository(db)
	pairs, err := lr.FindNearDuplicatePairs(context.Background(), tenantID, models.ScanThreshold)
	require.NoError(t, err)

	var sawJournalPair, sawLearningPair bool
	for _, p := range pairs {
		if (p.DocAID == jA && p.DocBID == jB) || (p.DocAID == jB && p.DocBID == jA) {
			sawJournalPair = true
		}
		if (p.DocAID == lA && p.DocBID == lB) || (p.DocAID == lB && p.DocBID == lA) {
			sawLearningPair = true
		}
	}
	require.False(t, sawJournalPair, "episodic (journal) near-duplicate pair must never be enqueued")
	require.True(t, sawLearningPair, "curated near-duplicate pair must still be found")
}

// TestCheckNearDuplicates_ExcludesEpisodic proves the on-demand lint_memory
// near-duplicate finding never surfaces a journal doc on either side of a
// pair, while a curated (reference) near-duplicate pair still reports.
func TestCheckNearDuplicates_ExcludesEpisodic(t *testing.T) {
	db := openLintPG(t)
	rng := rand.New(rand.NewSource(23))
	tenantID := seedTenant(t, db)
	t.Cleanup(func() { cleanupTenant(db, tenantID) })

	jBase := randUnit(rng)
	jA := seedTypedDoc(t, db, tenantID, "journal", models.DocTypeJournal, "cja-"+uuid.NewString(), jBase)
	jB := seedTypedDoc(t, db, tenantID, "journal", models.DocTypeJournal, "cjb-"+uuid.NewString(), nearDup(jBase, rng, 0.001))

	rBase := randUnit(rng)
	rA := seedTypedDoc(t, db, tenantID, "learnings", models.DocTypeReference, "cra-"+uuid.NewString(), rBase)
	rB := seedTypedDoc(t, db, tenantID, "learnings", models.DocTypeReference, "crb-"+uuid.NewString(), nearDup(rBase, rng, 0.001))

	var jaDoc, jbDoc, raDoc, rbDoc models.Document
	require.NoError(t, db.First(&jaDoc, jA).Error)
	require.NoError(t, db.First(&jbDoc, jB).Error)
	require.NoError(t, db.First(&raDoc, rA).Error)
	require.NoError(t, db.First(&rbDoc, rB).Error)
	jaPath, jbPath := jaDoc.Path(), jbDoc.Path()
	raPath, rbPath := raDoc.Path(), rbDoc.Path()

	lr := repository.NewLintRepository(db)
	th := repository.DefaultLintThresholds()
	th.DuplicateSimilarity = 0.90
	findings, err := lr.CheckNearDuplicates(context.Background(), tenantID, th)
	require.NoError(t, err)

	var sawJournal, sawReference bool
	for _, f := range findings {
		if f.DocumentPath == jaPath || f.DocumentPath == jbPath ||
			strings.Contains(f.Message, jaPath) || strings.Contains(f.Message, jbPath) {
			sawJournal = true
		}
		if f.DocumentPath == raPath || f.DocumentPath == rbPath ||
			strings.Contains(f.Message, raPath) || strings.Contains(f.Message, rbPath) {
			sawReference = true
		}
	}
	require.False(t, sawJournal, "episodic (journal) doc must never appear in a near_duplicate finding")
	require.True(t, sawReference, "curated near-duplicate pair must still be reported")
}
