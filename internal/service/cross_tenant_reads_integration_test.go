//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// TestCrossTenantReads_NoLeak is the load-bearing leak suite: a member of only
// tenant A never receives tenant B's document via Search, ListDocuments, or
// GetDocumentByID (even requesting the exact id), and a read filter naming a
// non-readable tenant yields empty/not-found — never that tenant's docs.
func TestCrossTenantReads_NoLeak(t *testing.T) {
	f := newAuthzFixture(t)
	token := "leaktok" + uuid.NewString()[:8]
	resB, err := f.svc.StoreDocument(ctxFor(f.tenantB, f.subjB), "learnings", nil,
		"leak-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", nil)
	require.NoError(t, err)
	docBID := resB.Document.ID

	ctxA := ctxFor(f.tenantA, f.subjA) // member of A only
	ctxB := ctxFor(f.tenantB, f.subjB) // member of B

	// Positive control: the owning member CAN fetch it by id.
	_, err = f.svc.GetDocumentByID(ctxB, docBID, false, "", nil)
	require.NoError(t, err, "owning member can read its tenant's doc by id")

	// Leak by id: a non-member requesting the exact id must get not-found.
	_, err = f.svc.GetDocumentByID(ctxA, docBID, false, "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "by-id fetch of another tenant's doc must be not-found")

	// Leak by search: tenant B's doc must not appear in a non-member's results.
	results, _, err := f.svc.Search(ctxA, token, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	for _, r := range results {
		require.NotEqual(t, f.tenantB, r.TenantID, "another tenant's doc must not leak via search")
	}

	// Leak by list: no tenant B docs in a non-member's listing.
	docs, err := f.svc.ListDocuments(ctxA, nil, nil, nil, 0, 0)
	require.NoError(t, err)
	for _, d := range docs {
		require.NotEqual(t, f.tenantB, d.TenantID, "another tenant's doc must not leak via list")
	}

	// Read filter to a non-readable tenant is empty / not-found, never a leak.
	filtered, _, err := f.svc.Search(ctxA, token, nil, nil, 20, false, "", &f.tenantB, false)
	require.NoError(t, err)
	require.Empty(t, filtered, "filter to a non-readable tenant returns empty")
	_, err = f.svc.GetDocumentByID(ctxA, docBID, false, "", &f.tenantB)
	require.ErrorIs(t, err, apperr.ErrNotFound, "id fetch under a non-readable filter is not-found")
}

// TestCrossTenantReads_FreshTenantPrivateByConstruction codifies the crucial
// invariant: a tenant created through the normal CreateTenant path (system edge
// + service-principal member only, NO viewer@user:* wildcard) is NOT readable by
// an unrelated, non-admin subject. Also audits that the ONLY viewer@user:*
// wildcard in the instance is the common pool — so a fresh tenant is private by
// construction and a future accidental per-tenant wildcard would be caught here.
func TestCrossTenantReads_FreshTenantPrivateByConstruction(t *testing.T) {
	f := newAuthzFixture(t)
	adminCtx := ctxFor(f.tenantA, f.admin)

	// Normal tenant provisioning (same seeding the admin API uses).
	newT, err := f.svc.CreateTenant(adminCtx, "priv-"+uuid.NewString(), "")
	require.NoError(t, err)

	// Seed a document inside T (admin overrides tenant_id to T).
	token := "privtok" + uuid.NewString()[:8]
	resT, err := f.svc.StoreDocument(adminCtx, "learnings", nil,
		"priv-"+uuid.NewString(), "# T\n\n## H\n"+token+" body", true, "seed", &newT.ID)
	require.NoError(t, err)
	docTID := resT.Document.ID

	// Stranger S: a plain non-admin, member of tenant A only, holding NO grant on T.
	ctxS := ctxFor(f.tenantA, f.subjA)

	// By id -> not found (even with the exact id).
	_, err = f.svc.GetDocumentByID(ctxS, docTID, false, "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound, "stranger cannot fetch a fresh tenant's doc by id")

	// Search / list never surface T's doc.
	results, _, err := f.svc.Search(ctxS, token, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	for _, r := range results {
		require.NotEqual(t, newT.ID, r.TenantID, "fresh tenant's doc must not leak via search")
	}
	docs, err := f.svc.ListDocuments(ctxS, nil, nil, nil, 0, 0)
	require.NoError(t, err)
	for _, d := range docs {
		require.NotEqual(t, newT.ID, d.TenantID, "fresh tenant's doc must not leak via list")
	}

	// A read filter naming T is empty / not-found — never T's doc, never a leak.
	filtered, _, err := f.svc.Search(ctxS, token, nil, nil, 20, false, "", &newT.ID, false)
	require.NoError(t, err)
	require.Empty(t, filtered, "filter to a non-readable fresh tenant returns empty")
	_, err = f.svc.GetDocumentByID(ctxS, docTID, false, "", &newT.ID)
	require.ErrorIs(t, err, apperr.ErrNotFound, "id fetch under a fresh-tenant filter is not-found")

	// Wildcard audit: the ONLY viewer@user:* tuple in the instance is the common
	// pool. A fresh tenant must not carry one.
	type tupleRow struct {
		ObjectType string
		ObjectID   string
	}
	var wildcards []tupleRow
	require.NoError(t, f.db.Table("relation_tuples").
		Select("object_type", "object_id").
		Where("relation = ? AND subject_type = ? AND subject_id = ?",
			authz.RelViewer, authz.TypeUser, authz.Wildcard).
		Find(&wildcards).Error)
	require.Len(t, wildcards, 1, "only the common pool may hold a viewer@user:* wildcard")
	require.Equal(t, authz.TypeTenant, wildcards[0].ObjectType)
	require.Equal(t, models.BootstrapTenantID.String(), wildcards[0].ObjectID)
}

// TestCrossTenantReads_LabeledByOwningTenant asserts read results carry their
// owning tenant's id, name, and type — on both the search result DTO and the
// document view.
func TestCrossTenantReads_LabeledByOwningTenant(t *testing.T) {
	f := newAuthzFixture(t)
	// Make subjA readable across A and B so a single search spans two tenants.
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	resB, err := f.svc.StoreDocument(ctxFor(f.tenantB, f.subjB), "learnings", nil,
		"lbl-"+uuid.NewString(), "# T\n\n## H\nbetaonly marker text", true, "seed", nil)
	require.NoError(t, err)

	var tenB models.Tenant
	require.NoError(t, f.db.First(&tenB, f.tenantB).Error)

	ctxA := ctxFor(f.tenantA, f.subjA)

	// SearchResult labeling: the cross-tenant hit carries tenant B's label.
	results, _, err := f.svc.Search(ctxA, "betaonly", nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	var sawB bool
	for _, r := range results {
		require.NotEqual(t, uuid.Nil, r.TenantID, "every result carries an owning tenant id")
		if r.TenantID == f.tenantB {
			sawB = true
			require.Equal(t, tenB.Name, r.TenantName, "result labeled with owning tenant name")
			require.Equal(t, tenB.Type, r.TenantType, "result labeled with owning tenant type")
		}
	}
	require.True(t, sawB, "the cross-tenant search result is present and labeled")

	// DocumentView labeling: get-by-path on B's doc carries the tenant label.
	view, err := f.svc.GetDocument(ctxA, resB.Document.Category, nil, resB.Document.Slug, false, "", nil)
	require.NoError(t, err)
	require.Equal(t, f.tenantB, view.TenantID)
	require.Equal(t, tenB.Name, view.TenantName)
	require.Equal(t, tenB.Type, view.TenantType)
}

// TestCrossTenantReads_PerTenantStaleness proves a mixed result set is treated
// per owning tenant: a hard-mode tenant's guarded section is withheld while an
// off-mode tenant's identical section passes through, in the same response.
func TestCrossTenantReads_PerTenantStaleness(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	// A = hard, B = off.
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantA).
		Update("staleness_mode", models.StalenessModeHard).Error)
	require.NoError(t, f.db.Model(&models.Tenant{}).Where("id = ?", f.tenantB).
		Update("staleness_mode", models.StalenessModeOff).Error)

	token := "staletok" + uuid.NewString()[:8]
	// Content mentions a code path -> guard-eligible once stale.
	body := "internal/service/memory.go is where it lives " + token
	resA, err := f.svc.StoreDocument(ctxFor(f.tenantA, f.subjA), "learnings", nil,
		"sa-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)
	resB, err := f.svc.StoreDocument(ctxFor(f.tenantB, f.subjB), "learnings", nil,
		"sb-"+uuid.NewString(), "# T\n\n## H\n"+body, true, "seed", nil)
	require.NoError(t, err)

	// Backdate both sections' verified_at well past the reference threshold.
	secA := resA.Document.Sections[0].ID
	secB := resB.Document.Sections[0].ID
	old := time.Now().Add(-400 * 24 * time.Hour)
	require.NoError(t, f.db.Model(&models.Section{}).
		Where("id IN ?", []uuid.UUID{secA, secB}).Update("verified_at", old).Error)

	// Key by section id, not tenant id: subjA can also read the shared fixture's
	// other docs in tenants A/B, which the semantic arm may surface, so a
	// tenant-keyed last-write-wins lookup could latch onto a non-target section.
	// secA/secB are the sections we backdated; the retry absorbs a rare under-return
	// of the unique-token keyword hits (lex-only score 0.6 > scoreFloor 0.4).
	var ra, rb repository.SearchResult
	var okA, okB bool
	for attempt := 0; attempt < 5; attempt++ {
		results, _, err := f.svc.Search(ctxFor(f.tenantA, f.subjA), token, nil, nil, 20, false, "", nil, false)
		require.NoError(t, err)
		bySection := map[uuid.UUID]repository.SearchResult{}
		for _, r := range results {
			bySection[r.SectionID] = r
		}
		ra, okA = bySection[secA]
		rb, okB = bySection[secB]
		if okA && okB {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	require.True(t, okA, "hard-mode tenant section present")
	require.True(t, okB, "off-mode tenant section present")

	// Hard-mode tenant: guarded — content withheld, status set.
	require.Equal(t, "needs_verification", ra.Status, "hard-mode tenant result is guarded")
	require.Empty(t, ra.Content, "guarded content is withheld")

	// Off-mode tenant: identical section passes through untouched.
	require.NotEmpty(t, rb.Content, "off-mode tenant result keeps its content")
	require.Empty(t, rb.Status, "off-mode tenant result is not guarded")
}
