//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
)

// TestEpisodicWithholding_JournalNeverExpires: on a hard-mode tenant an aged
// journal section (verification 0, expiration disabled) is served in full, while
// an aged learning section with an expiration age set is withheld (status=expired).
func TestEpisodicWithholding_JournalNeverExpires(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)

	// Opt learning into the hard withhold (expiration >= its verification age 180).
	adminCtx := ctxFor(f.tenantA, f.admin)
	require.NoError(t, f.svc.SetDocTypePolicy(adminCtx,
		models.DocTypePolicy{DocType: models.DocTypeLearning, ExpirationAgeDays: iptrLocal(200)}))

	token := "journaltok" + uuid.NewString()[:8]
	body := "some durable knowledge worth keeping " + token

	resJ, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "journal", nil,
		"2026-09-01", "# T\n\n## H\n"+body, true, "seed", nil, nil)
	require.NoError(t, err)
	require.Equal(t, models.DocTypeJournal, resJ.Document.DocType)

	resL, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil,
		"l-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil, nil)
	require.NoError(t, err)

	// Backdate both sections' verified_at well past any doc_type threshold.
	old := time.Now().Add(-400 * 24 * time.Hour)
	secJ := resJ.Document.Sections[0].ID
	secL := resL.Document.Sections[0].ID
	require.NoError(t, f.db.Model(&models.Section{}).
		Where("id IN ?", []uuid.UUID{secJ, secL}).Update("verified_at", old).Error)

	viewJ, err := f.svc.GetDocument(ctxFor(f.tenantA, f.subjA), "journal", nil, resJ.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, viewJ.Sections[0].Status, "episodic (journal) section never expires")
	require.NotEmpty(t, viewJ.Sections[0].Content, "episodic (journal) content is returned in full")

	viewL, err := f.svc.GetDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil, resL.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, "expired", viewL.Sections[0].Status, "aged learning past expiration is withheld")
	require.Empty(t, viewL.Sections[0].Content, "expired content is withheld")
	require.NotEmpty(t, viewL.Sections[0].Preview, "expired section carries a heading preview")
}
