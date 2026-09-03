//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

// storeHandoff stores a handoff at handoffs/<project>/<slug> and returns the result.
func storeHandoff(t *testing.T, f *authzFixture, ctx context.Context, project, slug, content string) *service.StoreResult {
	t.Helper()
	p := project
	res, err := f.svc.StoreDocument(ctx, "handoffs", &p, slug, content, true, "seed", nil, nil)
	require.NoError(t, err)
	require.NotNil(t, res.Document)
	return res
}

// continuesFromTargets returns the outgoing continues_from targets of docID.
func continuesFromTargets(t *testing.T, f *authzFixture, ctx context.Context, docID uuid.UUID) []uuid.UUID {
	t.Helper()
	list, err := f.svc.ListDocumentEdges(ctx, docID, nil)
	require.NoError(t, err)
	var out []uuid.UUID
	for _, e := range list {
		if e.EdgeType == models.EdgeContinuesFrom && e.Direction == "outgoing" {
			out = append(out, e.OtherDocumentID)
		}
	}
	return out
}

// TestHandoff_DocTypeAndDupGuardExempt proves a handoffs/<project>/<slug> path
// infers doc_type=handoff and near-duplicate handoffs are never dup-guard-blocked
// (episodic exemption), while a curated near-dup still is.
func TestHandoff_DocTypeAndDupGuardExempt(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("duplicate_guard", true).Error)
	ctx := ctxFor(f.tenantA, f.subjA)
	project := "proj-" + uuid.NewString()[:8]

	body := "# Handoff\n\n## summary\nwhere we left off " + uuid.NewString()[:6]
	res := storeHandoff(t, f, ctx, project, "h-"+uuid.NewString(), body)
	require.Equal(t, models.DocTypeHandoff, res.Document.DocType)

	// A byte-identical second handoff at a new slug must store despite guard on.
	res2, err := f.svc.StoreDocument(ctx, "handoffs", &project, "h-"+uuid.NewString(), body, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res2.Status, "episodic handoff must be exempt from the duplicate guard")

	// Control: an identical curated (learnings) doc IS flagged with the guard on.
	learn := "# L\n\n## s\nidentical curated body " + uuid.NewString()[:6]
	_, err = f.svc.StoreDocument(ctx, "learnings", nil, "l-"+uuid.NewString(), learn, false, "", nil, nil)
	require.NoError(t, err)
	dup, err := f.svc.StoreDocument(ctx, "learnings", nil, "l-"+uuid.NewString(), learn, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "similar_exists", dup.Status, "curated near-dup must still be guarded")
}

// TestHandoff_NotStaleWithheld proves a stale, code-path-mentioning handoff
// section is returned in full on a hard-mode tenant (episodic ⇒ never withheld).
func TestHandoff_NotStaleWithheld(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)
	ctx := ctxFor(f.tenantA, f.subjA)
	project := "proj-" + uuid.NewString()[:8]

	body := "internal/service/memory.go is where it lives " + uuid.NewString()[:6]
	res := storeHandoff(t, f, ctx, project, "h-"+uuid.NewString(), "# H\n\n## H\n"+body)

	old := time.Now().Add(-400 * 24 * time.Hour)
	require.NoError(t, f.db.Model(&models.Section{}).
		Where("id = ?", res.Document.Sections[0].ID).Update("verified_at", old).Error)

	view, err := f.svc.GetDocument(ctx, "handoffs", &project, res.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Empty(t, view.Sections[0].Status, "episodic handoff section must never be guarded")
	require.NotEmpty(t, view.Sections[0].Content, "episodic handoff content must be returned in full")
}

// TestHandoff_AutoChain: a new handoff auto-links continues_from to the prior
// latest (not archiving it), the first links nothing, a blocked edge-write still
// stores the handoff, and re-storing a slug (an update) adds no new chain node.
func TestHandoff_AutoChain(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	project := "proj-" + uuid.NewString()[:8]

	h1 := storeHandoff(t, f, ctx, project, "h1-"+uuid.NewString(), "# H1\n\n## summary\nfirst")
	require.Empty(t, continuesFromTargets(t, f, ctx, h1.Document.ID), "first handoff links nothing")

	h2 := storeHandoff(t, f, ctx, project, "h2-"+uuid.NewString(), "# H2\n\n## summary\nsecond")
	require.Equal(t, []uuid.UUID{h1.Document.ID}, continuesFromTargets(t, f, ctx, h2.Document.ID),
		"second handoff continues_from the first")

	var d1 models.Document
	require.NoError(t, f.db.First(&d1, h1.Document.ID).Error)
	require.Nil(t, d1.ArchivedAt, "continues_from must NOT archive the prior handoff")

	// Best-effort: a blocked edge insert still stores the handoff with a warning.
	require.NoError(t, f.db.Exec(`CREATE OR REPLACE FUNCTION handoff_block_edge() RETURNS trigger AS $$ BEGIN IF NEW.edge_type = 'continues_from' THEN RAISE EXCEPTION 'blocked'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER handoff_block_edge BEFORE INSERT ON document_edges FOR EACH ROW EXECUTE FUNCTION handoff_block_edge()`).Error)
	h3, err := f.svc.StoreDocument(ctx, "handoffs", &project, "h3-"+uuid.NewString(), "# H3\n\n## summary\nthird", true, "seed", nil, nil)
	require.NoError(t, err, "a blocked auto-chain edge must not fail the handoff write")
	require.Equal(t, "ok", h3.Status)
	require.NotEmpty(t, h3.Warnings, "the blocked auto-chain must surface a warning")
	require.NoError(t, f.db.Exec(`DROP TRIGGER IF EXISTS handoff_block_edge ON document_edges`).Error)
	require.NoError(t, f.db.Exec(`DROP FUNCTION IF EXISTS handoff_block_edge()`).Error)

	var d3count int64
	require.NoError(t, f.db.Model(&models.Document{}).Where("id = ?", h3.Document.ID).Count(&d3count).Error)
	require.Equal(t, int64(1), d3count, "the handoff itself is stored despite the blocked edge")

	// Re-storing h2's slug is an update: no new chain node is created.
	_, err = f.svc.StoreDocument(ctx, "handoffs", &project, h2.Document.Slug, "# H2\n\n## summary\nsecond edited", true, "seed", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{h1.Document.ID}, continuesFromTargets(t, f, ctx, h2.Document.ID),
		"re-storing a slug adds no new chain node")
}

// TestHandoff_Resume proves resume returns the newest handoff (full content),
// walks the continues_from chain in order to the requested depth, and returns an
// empty result (no error) for a project with no handoffs.
func TestHandoff_Resume(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	project := "proj-" + uuid.NewString()[:8]

	h1 := storeHandoff(t, f, ctx, project, "h1-"+uuid.NewString(), "# H1\n\n## summary\nfirst")
	h2 := storeHandoff(t, f, ctx, project, "h2-"+uuid.NewString(), "# H2\n\n## summary\nsecond")
	h3 := storeHandoff(t, f, ctx, project, "h3-"+uuid.NewString(), "# H3\n\n## summary\nthird")

	full, err := f.svc.Resume(ctx, &project, nil, 10)
	require.NoError(t, err)
	require.NotNil(t, full.Latest)
	require.Equal(t, h3.Document.ID, full.Latest.ID, "resume returns the newest handoff")
	require.NotEmpty(t, full.Latest.Sections, "latest handoff carries full content")
	require.Equal(t, []uuid.UUID{h2.Document.ID, h1.Document.ID},
		[]uuid.UUID{full.Chain[0].ID, full.Chain[1].ID}, "chain is ordered newest-first")

	shallow, err := f.svc.Resume(ctx, &project, nil, 2)
	require.NoError(t, err)
	require.Equal(t, h3.Document.ID, shallow.Latest.ID)
	require.Len(t, shallow.Chain, 1, "depth 2 = latest + one prior")
	require.Equal(t, h2.Document.ID, shallow.Chain[0].ID)

	empty := "proj-empty-" + uuid.NewString()[:8]
	none, err := f.svc.Resume(ctx, &empty, nil, 10)
	require.NoError(t, err, "resume on a project with no handoffs is not an error")
	require.Nil(t, none.Latest)
	require.Empty(t, none.Chain)
}

// TestHandoff_ResumeAuthzNoLeak proves a caller who cannot read a tenant's
// handoffs gets an empty result (no handoff, no error, no existence leak).
func TestHandoff_ResumeAuthzNoLeak(t *testing.T) {
	f := newAuthzFixture(t)
	project := "proj-" + uuid.NewString()[:8]
	storeHandoff(t, f, ctxFor(f.tenantA, f.subjA), project, "h-"+uuid.NewString(), "# H\n\n## summary\nsecret")

	// subjB cannot read tenant A; a tenant_id override to A yields an empty scope.
	other := ctxFor(f.tenantB, f.subjB)
	res, err := f.svc.Resume(other, &project, &f.tenantA, 10)
	require.NoError(t, err, "non-reader resume reveals nothing via a clean empty result")
	require.Nil(t, res.Latest, "no handoff content is returned to a non-reader")
	require.Empty(t, res.Chain)
}

// TestHandoff_SeedIdempotent proves the handoff staleness-threshold seed exists
// after migration and that re-running it changes nothing (ON CONFLICT DO NOTHING).
func TestHandoff_SeedIdempotent(t *testing.T) {
	f := newAuthzFixture(t)

	var days *int
	require.NoError(t, f.db.Raw(`SELECT verification_age_days FROM doc_type_policies WHERE doc_type = ?`, models.DocTypeHandoff).Scan(&days).Error)
	require.NotNil(t, days)
	require.Equal(t, 0, *days, "handoff seeded with verification_age_days 0 (never), not the old 3650")

	for i := 0; i < 2; i++ {
		require.NoError(t, f.db.Exec(
			`INSERT INTO doc_type_policies (doc_type, verification_age_days, rules) VALUES (?, ?, '{}') ON CONFLICT (doc_type) DO NOTHING`,
			models.DocTypeHandoff, 9999).Error)
	}
	require.NoError(t, f.db.Raw(`SELECT verification_age_days FROM doc_type_policies WHERE doc_type = ?`, models.DocTypeHandoff).Scan(&days).Error)
	require.Equal(t, 0, *days, "re-running the seed is idempotent")
}

// TestHandoff_AutoChainProjectScoped proves auto-chain matches the exact project:
// a project-less handoff (subcategory NULL) never links across a project, and two
// project-less handoffs chain to each other (NULL matches NULL).
func TestHandoff_AutoChainProjectScoped(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	projA := "projA-" + uuid.NewString()[:8]

	// A handoff under projA must NOT be linked to by one under a different project
	// (the bug a nil-means-any-project lookup would cause). handoffs require a
	// subcategory now, so scope isolation is asserted across two real projects.
	storeHandoff(t, f, ctx, projA, "a-"+uuid.NewString(), "# A\n\n## summary\nA")
	projB := "projB-" + uuid.NewString()[:8]

	g1 := storeHandoff(t, f, ctx, projB, "g1-"+uuid.NewString(), "# G1\n\n## summary\ng1")
	require.Empty(t, continuesFromTargets(t, f, ctx, g1.Document.ID),
		"a projB handoff must not chain across to projA")

	g2 := storeHandoff(t, f, ctx, projB, "g2-"+uuid.NewString(), "# G2\n\n## summary\ng2")
	require.Equal(t, []uuid.UUID{g1.Document.ID}, continuesFromTargets(t, f, ctx, g2.Document.ID),
		"handoffs in the same project chain to each other")
}

// TestHandoff_ResumeCycleSafe proves the chain walk terminates on a cyclic
// continues_from graph (bounded by the seen-set, never loops).
func TestHandoff_ResumeCycleSafe(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	project := "proj-" + uuid.NewString()[:8]

	h1 := storeHandoff(t, f, ctx, project, "h1-"+uuid.NewString(), "# H1\n\n## summary\nfirst")
	h2 := storeHandoff(t, f, ctx, project, "h2-"+uuid.NewString(), "# H2\n\n## summary\nsecond")

	// h2 already continues_from h1 (auto); add the reverse to form a cycle.
	_, err := f.svc.CreateEdge(ctx, h1.Document.ID, h2.Document.ID, models.EdgeContinuesFrom, nil)
	require.NoError(t, err)

	res, err := f.svc.Resume(ctx, &project, nil, 50)
	require.NoError(t, err)
	require.Equal(t, h2.Document.ID, res.Latest.ID)
	require.Len(t, res.Chain, 1, "cycle walk stops at the first revisit, no loop")
	require.Equal(t, h1.Document.ID, res.Chain[0].ID)
}
