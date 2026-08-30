//go:build integration

package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/service"
)

// markerEmbedder maps a marker substring found in a section body to a crafted
// vector, so tests can dictate exact cosine relationships (the FakeEmbedder's
// sha256 vectors cannot). Each section body must carry exactly one marker.
type markerEmbedder struct {
	dim  int
	vecs map[string]pgvector.Vector
}

func (e *markerEmbedder) Dimensions() int { return e.dim }

func (e *markerEmbedder) Embed(_ context.Context, text string) (pgvector.Vector, error) {
	for marker, v := range e.vecs {
		if strings.Contains(text, marker) {
			return v, nil
		}
	}
	// Unmarked text (titles/filler-only): a fixed direction orthogonal to markers.
	return mkvec(e.dim, 0, 0, 0, 0, 1), nil
}

// mkvec builds a dim-length vector with the leading components set to comps.
func mkvec(dim int, comps ...float32) pgvector.Vector {
	v := make([]float32, dim)
	copy(v, comps)
	return pgvector.NewVector(v)
}

func f64ptr(f float64) *float64 { return &f }

func newVecTestSvc(db *gorm.DB, store authz.Store, emb service.EmbeddingProvider) *service.MemoryService {
	return service.NewMemoryService(
		db,
		repository.NewDocumentRepository(db),
		repository.NewSectionRepository(db),
		emb,
		repository.NewTenantRepository(db),
		repository.NewAPIKeyRepository(db),
		nil, nil, nil, nil,
		nil, nil,
		store,
	)
}

// dupGuardEmbedder is the shared marker->vector map for these tests. Directions:
// MKA..MKD are the standard basis; MKE=(1,1,1,1) is the L-centroid direction;
// MKM/MKN/MKP = cos 0.65/0.80/0.90 to MKA.
func dupGuardEmbedder() *markerEmbedder {
	return &markerEmbedder{dim: fakeDim, vecs: map[string]pgvector.Vector{
		"MKA": mkvec(fakeDim, 1, 0, 0, 0),
		"MKB": mkvec(fakeDim, 0, 1, 0, 0),
		"MKC": mkvec(fakeDim, 0, 0, 1, 0),
		"MKD": mkvec(fakeDim, 0, 0, 0, 1),
		"MKE": mkvec(fakeDim, 1, 1, 1, 1),
		"MKN": mkvec(fakeDim, 0.8, 0.6, 0, 0),
		"MKM": mkvec(fakeDim, 0.65, 0.76, 0, 0),
		"MKP": mkvec(fakeDim, 0.9, 0.43589, 0, 0),
	}}
}

func dgTenant(t *testing.T, db *gorm.DB, guard bool) models.Tenant {
	t.Helper()
	tenant := models.Tenant{ID: uuid.New(), Name: "dg-" + uuid.NewString(), DuplicateGuard: guard}
	require.NoError(t, db.Create(&tenant).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM documents WHERE tenant_id = ?", tenant.ID) })
	return tenant
}

func dgCtx(tid uuid.UUID) context.Context {
	return auth.WithLocalAdmin(auth.WithTenantID(context.Background(), tid))
}

func dgCandidate(res *service.StoreResult, path string) *repository.SimilarityCandidate {
	for i := range res.Candidates {
		if res.Candidates[i].Path() == path {
			return &res.Candidates[i]
		}
	}
	return nil
}

// TestDuplicateGuard_CentroidNotLengthBiased proves the guard scores whole-document
// centroid similarity: a long doc with one incidentally-matching section is NOT
// flagged (the old MAX-over-pairs would have), while a genuine whole-doc dup IS.
func TestDuplicateGuard_CentroidNotLengthBiased(t *testing.T) {
	db := openServicePG(t)
	svc := newVecTestSvc(db, authz.NewPostgresStore(db), dupGuardEmbedder())
	tenant := dgTenant(t, db, true)
	ctx := dgCtx(tenant.ID)

	long := "# Long\n\n## s1\nMKA a\n## s2\nMKB b\n## s3\nMKC c\n## s4\nMKD d\n"
	res, err := svc.StoreDocument(ctx, "learnings", nil, "dg-long", long, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// One section (MKA) is identical to a long-doc section, but the new centroid is
	// only ~0.5 to the long doc's centroid -> not a duplicate.
	res, err = svc.StoreDocument(ctx, "learnings", nil, "dg-incidental", "# Inc\n\n## s\nMKA z\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "incidental single-section overlap must not flag a long doc")

	// A whole-doc match (centroid == long doc's centroid direction) IS flagged.
	res, err = svc.StoreDocument(ctx, "learnings", nil, "dg-genuine", "# Gen\n\n## s\nMKE whole\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "similar_exists", res.Status)
	cand := dgCandidate(res, "learnings/dg-long")
	require.NotNil(t, cand, "genuine whole-doc near-duplicate must flag the long doc")
	require.GreaterOrEqual(t, cand.Similarity, 0.70)
}

// TestDuplicateGuard_HashShortCircuit proves a byte-identical re-save at a different
// path is caught by the content-hash lookup (similarity 1.0), and that a doc with
// no stored hash still evaluates through the centroid guard.
func TestDuplicateGuard_HashShortCircuit(t *testing.T) {
	db := openServicePG(t)
	svc := newVecTestSvc(db, authz.NewPostgresStore(db), dupGuardEmbedder())
	tenant := dgTenant(t, db, true)
	ctx := dgCtx(tenant.ID)

	content := "# A\n\n## s\nMKA body\n"
	res, err := svc.StoreDocument(ctx, "learnings", nil, "dg-hashA", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	// Byte-identical content at a different path -> hash short-circuit, sim 1.0.
	res, err = svc.StoreDocument(ctx, "learnings", nil, "dg-hashB", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "similar_exists", res.Status)
	cand := dgCandidate(res, "learnings/dg-hashA")
	require.NotNil(t, cand)
	require.Equal(t, 1.0, cand.Similarity, "exact-hash hit reports similarity 1.0")

	// A doc whose hash is NULL (pre-migration) is not hash-matched, but the centroid
	// guard still evaluates it. Store X, null its hash, then save a non-identical
	// centroid near-dup Y.
	_, err = svc.StoreDocument(ctx, "learnings", nil, "dg-nohashX", "# X\n\n## s\nMKC xbody\n", false, "", nil, nil)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE documents SET content_hash = NULL WHERE tenant_id = ? AND slug = ?", tenant.ID, "dg-nohashX").Error)

	res, err = svc.StoreDocument(ctx, "learnings", nil, "dg-nohashY", "# Y\n\n## s\nMKC ybody\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "similar_exists", res.Status)
	require.NotNil(t, dgCandidate(res, "learnings/dg-nohashX"), "hash-less doc must still be caught by the centroid guard")
}

// TestDuplicateGuard_PerTenantThreshold proves the threshold resolves as
// COALESCE(override, global default 0.85): a 0.90 tenant ignores a 0.80 near-dup,
// an unset tenant inherits 0.85, bad input is rejected, and clearing reverts.
func TestDuplicateGuard_PerTenantThreshold(t *testing.T) {
	db := openServicePG(t)
	svc := newVecTestSvc(db, authz.NewPostgresStore(db), dupGuardEmbedder())
	adminCtx := auth.WithLocalAdmin(context.Background())

	// Tightened tenant: override 0.90, a 0.80 near-dup must not flag.
	tight := dgTenant(t, db, true)
	_, err := svc.UpdateTenant(adminCtx, tight.ID, service.UpdateTenantFields{DuplicateThreshold: f64ptr(0.90)})
	require.NoError(t, err)
	tctx := dgCtx(tight.ID)
	_, err = svc.StoreDocument(tctx, "learnings", nil, "dg-base", "# B\n\n## s\nMKA base\n", false, "", nil, nil)
	require.NoError(t, err)
	res, err := svc.StoreDocument(tctx, "learnings", nil, "dg-near", "# N\n\n## s\nMKN near\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "0.80 near-dup must not flag at a 0.90 threshold")

	// Unset tenant inherits the 0.85 global default: 0.80 does not flag, 0.90 does.
	def := dgTenant(t, db, true)
	require.Nil(t, def.DuplicateThreshold, "fresh tenant has no override (inherits the global default)")
	dctx := dgCtx(def.ID)
	_, err = svc.StoreDocument(dctx, "learnings", nil, "dg-base", "# B\n\n## s\nMKA base\n", false, "", nil, nil)
	require.NoError(t, err)
	res, err = svc.StoreDocument(dctx, "learnings", nil, "dg-mid", "# N\n\n## s\nMKN mid\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "0.80 near-dup is below the 0.85 inherited default")
	res, err = svc.StoreDocument(dctx, "learnings", nil, "dg-near", "# P\n\n## s\nMKP near\n", false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "similar_exists", res.Status, "0.90 near-dup flags at the 0.85 inherited default")

	// Out-of-range updates are rejected; a valid one persists.
	for _, bad := range []float64{0, -0.1, 1.5} {
		_, err = svc.UpdateTenant(adminCtx, def.ID, service.UpdateTenantFields{DuplicateThreshold: f64ptr(bad)})
		require.ErrorIs(t, err, apperr.ErrInvalidInput, "threshold %v must be rejected", bad)
	}
	updated, err := svc.UpdateTenant(adminCtx, def.ID, service.UpdateTenantFields{DuplicateThreshold: f64ptr(0.95)})
	require.NoError(t, err)
	require.NotNil(t, updated.DuplicateThreshold)
	require.InDelta(t, 0.95, *updated.DuplicateThreshold, 1e-6)

	// Clearing the override reverts to NULL (inherit the global default).
	cleared, err := svc.UpdateTenant(adminCtx, def.ID, service.UpdateTenantFields{ClearDuplicateThreshold: true})
	require.NoError(t, err)
	require.Nil(t, cleared.DuplicateThreshold, "cleared override reverts to inherit")
}

// TestDuplicateGuard_ScopePreserved proves the guard's scope rules survive: off =
// no check, an in-place update never self-flags, episodic types are exempt, and the
// common pool is never a duplicate source.
func TestDuplicateGuard_ScopePreserved(t *testing.T) {
	db := openServicePG(t)
	svc := newVecTestSvc(db, authz.NewPostgresStore(db), dupGuardEmbedder())

	// Guard off: an identical second doc is stored, not flagged.
	off := dgTenant(t, db, false)
	octx := dgCtx(off.ID)
	content := "# C\n\n## s\nMKA body\n"
	_, err := svc.StoreDocument(octx, "learnings", nil, "dg-off-a", content, false, "", nil, nil)
	require.NoError(t, err)
	res, err := svc.StoreDocument(octx, "learnings", nil, "dg-off-b", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "guard off -> no check")

	// Guard on for the remaining scope cases.
	on := dgTenant(t, db, true)
	nctx := dgCtx(on.ID)

	// Update in place: same path, identical content -> excluded from both lookups.
	first, err := svc.StoreDocument(nctx, "learnings", nil, "dg-upd", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", first.Status)
	again, err := svc.StoreDocument(nctx, "learnings", nil, "dg-upd", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", again.Status, "re-saving a doc must not flag itself")
	require.Equal(t, first.Document.ID, again.Document.ID, "same path is an update, not a new doc")

	// Episodic (journal) is exempt: two identical journals both store.
	_, err = svc.StoreDocument(nctx, "journal", nil, "dg-j1", content, false, "", nil, nil)
	require.NoError(t, err)
	res, err = svc.StoreDocument(nctx, "journal", nil, "dg-j2", content, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "episodic doc types are exempt from the guard")

	// Common pool is never a source: seed an identical common-pool doc (distinct
	// content/direction so only the common-pool copy could match), then a tenant
	// save of the same content is not flagged.
	cpContent := "# CP\n\n## s\nMKB cp\n"
	cpCtx := dgCtx(models.BootstrapTenantID)
	cp, err := svc.StoreDocument(cpCtx, "learnings", nil, "dg-cp-"+uuid.NewString(), cpContent, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", cp.Status)
	t.Cleanup(func() { db.Exec("DELETE FROM documents WHERE id = ?", cp.Document.ID) })
	res, err = svc.StoreDocument(nctx, "learnings", nil, "dg-cp-new", cpContent, false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status, "common-pool docs must never be flagged as duplicates")
}
