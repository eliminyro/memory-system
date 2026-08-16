//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestEpisodicWithholding_JournalNeverGuarded proves a stale, code-path-mentioning
// journal section is returned in full on a hard-mode tenant (never withheld as
// needs_verification), while an identical curated (learning) section on the same
// tenant is still guarded — mirroring TestCrossTenantReads_PerTenantStaleness.
func TestEpisodicWithholding_JournalNeverGuarded(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)

	token := "journaltok" + uuid.NewString()[:8]
	// Content mentions a code path -> guard-eligible once stale.
	body := "internal/service/memory.go is where it lives " + token

	resJ, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "journal", nil,
		"j-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	require.Equal(t, models.DocTypeJournal, resJ.Document.DocType)

	resL, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil,
		"l-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)

	// Backdate both sections' verified_at well past any doc_type threshold.
	old := time.Now().Add(-400 * 24 * time.Hour)
	secJ := resJ.Document.Sections[0].ID
	secL := resL.Document.Sections[0].ID
	require.NoError(t, f.db.Model(&models.Section{}).
		Where("id IN ?", []uuid.UUID{secJ, secL}).Update("verified_at", old).Error)

	viewJ, err := f.svc.GetDocument(ctxFor(f.tenantA, f.subjA), "journal", nil, resJ.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, viewJ.Sections[0].Status, "episodic (journal) section must never be guarded")
	require.NotEmpty(t, viewJ.Sections[0].Content, "episodic (journal) content must be returned in full")

	viewL, err := f.svc.GetDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil, resL.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, "needs_verification", viewL.Sections[0].Status, "curated stale code-path section must still be guarded")
	require.Empty(t, viewL.Sections[0].Content, "guarded curated content is withheld")
}
