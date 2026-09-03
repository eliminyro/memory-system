package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/authzseed"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
	"github.com/eliminyro/memory-system/internal/staleness"
)

// ctReadSvc wires a MemoryService with ONLY an in-memory authz store — no DB —
// enough to exercise the read-scope resolvers (readableTenants/readScope) in
// isolation. Mirrors newACLUnitSvc.
func ctReadSvc(store authz.Store) *MemoryService {
	return NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, store)
}

func ctCtx(home uuid.UUID, subj string) context.Context {
	c := auth.WithTenantID(context.Background(), home)
	return auth.WithSubject(c, auth.Subject{Type: auth.SubjectTypeUser, ID: subj})
}

func ctHas(ids []uuid.UUID, want uuid.UUID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// --- readableTenants: the authz-derived aggregation set ---

func TestReadableTenants_MemberOfT1(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t1 := uuid.New(), uuid.New()
	subj := "user-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(t1, subj)))

	got, err := ctReadSvc(store).readableTenants(ctCtx(home, subj))
	require.NoError(t, err)

	require.Len(t, got, 3, "home + common + the directly-granted tenant")
	require.True(t, ctHas(got, home), "home tenant always readable")
	require.True(t, ctHas(got, models.BootstrapTenantID), "common pool always readable")
	require.True(t, ctHas(got, t1), "directly-granted tenant readable")
}

// A system admin who is a DIRECT member of only T1 must aggregate over
// {home, common, T1} — NOT every tenant in the instance. The set is derived
// from the subject's direct tuples (ReadBySubject), and system#admin has object
// type "system" so it is skipped: a naive "admin sees all" impl would leak T2.
func TestReadableTenants_AdminNotExpanded(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t1, t2 := uuid.New(), uuid.New(), uuid.New()
	subj := "admin-" + uuid.NewString()
	ctx := context.Background()
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(subj)))
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(t1, subj)))
	// T2 exists with the usual system edge but the subject has no direct grant.
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(t2)))

	got, err := ctReadSvc(store).readableTenants(ctCtx(home, subj))
	require.NoError(t, err)

	require.Len(t, got, 3, "admin aggregation stays bounded to home+common+direct grants")
	require.True(t, ctHas(got, t1))
	require.False(t, ctHas(got, t2), "system admin must NOT be expanded into every tenant")
}

// A tenant created via the normal CreateTenant path carries ONLY its default
// seeding — a system edge and a service-principal member, NO viewer@user:*
// wildcard — so it is private to an unrelated subject by construction. Guards
// against a future accidental per-tenant viewer wildcard turning aggregation
// into a leak.
func TestReadableTenants_FreshTenantIsPrivate(t *testing.T) {
	store := authz.NewMemoryStore()
	home, tT := uuid.New(), uuid.New()
	stranger := "stranger-" + uuid.NewString()
	ctx := context.Background()
	// Exactly what CreateTenant seeds — and nothing else.
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(tT)))
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(tT, authz.ServicePrincipalID(tT.String()))))

	got, err := ctReadSvc(store).readableTenants(ctCtx(home, stranger))
	require.NoError(t, err)
	require.Len(t, got, 2, "stranger sees only home + common")
	require.False(t, ctHas(got, tT), "a normally-seeded tenant must be private to a stranger")
}

func TestReadableTenants_NoMembership(t *testing.T) {
	store := authz.NewMemoryStore()
	home := uuid.New()
	subj := "user-" + uuid.NewString()

	got, err := ctReadSvc(store).readableTenants(ctCtx(home, subj))
	require.NoError(t, err)

	require.Len(t, got, 2, "no direct grants -> just home + common")
	require.True(t, ctHas(got, home))
	require.True(t, ctHas(got, models.BootstrapTenantID))
}

// --- readScope: nil aggregates, a set narrows, non-readable is empty ---

func TestReadScope_NilAggregates(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t1 := uuid.New(), uuid.New()
	subj := "user-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(t1, subj)))

	got, err := ctReadSvc(store).readScope(ctCtx(home, subj), nil)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.True(t, ctHas(got, home) && ctHas(got, models.BootstrapTenantID) && ctHas(got, t1))
}

func TestReadScope_FilterReadableTenant(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t1 := uuid.New(), uuid.New()
	subj := "user-" + uuid.NewString()
	require.NoError(t, store.Write(context.Background(), authzseed.TenantMember(t1, subj)))

	got, err := ctReadSvc(store).readScope(ctCtx(home, subj), &t1)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{t1}, got, "filter narrows to the one readable tenant")
}

func TestReadScope_FilterHomeAndCommon(t *testing.T) {
	store := authz.NewMemoryStore()
	home := uuid.New()
	subj := "user-" + uuid.NewString()
	svc := ctReadSvc(store)

	gotHome, err := svc.readScope(ctCtx(home, subj), &home)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{home}, gotHome)

	boot := models.BootstrapTenantID
	gotCommon, err := svc.readScope(ctCtx(home, subj), &boot)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{boot}, gotCommon)
}

// The load-bearing leak guard at the scope layer: a T1-only member filtering to
// T2 gets an EMPTY scope — never T2, never an existence-revealing error.
func TestReadScope_FilterNonReadableIsEmpty(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t1, t2 := uuid.New(), uuid.New(), uuid.New()
	subj := "user-" + uuid.NewString()
	ctx := context.Background()
	require.NoError(t, store.Write(ctx, authzseed.TenantMember(t1, subj)))
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(t2)))

	got, err := ctReadSvc(store).readScope(ctCtx(home, subj), &t2)
	require.NoError(t, err, "non-readable filter must NOT error (no existence leak)")
	require.Empty(t, got, "non-readable filter yields an empty scope, never that tenant's docs")
}

// A system admin may deliberately target ANY tenant via the filter (admin ⇒
// viewer), preserving today's admin single-tenant read.
func TestReadScope_AdminFilterAnyTenant(t *testing.T) {
	store := authz.NewMemoryStore()
	home, t2 := uuid.New(), uuid.New()
	subj := "admin-" + uuid.NewString()
	ctx := context.Background()
	require.NoError(t, store.Write(ctx, authzseed.SystemAdmin(subj)))
	require.NoError(t, store.Write(ctx, authzseed.TenantSystemEdge(t2)))

	got, err := ctReadSvc(store).readScope(ctCtx(home, subj), &t2)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{t2}, got, "admin may filter to a tenant they are not a member of")
}

// --- per-tenant staleness routing (DB-free branches) ---

func TestApplyStaleness_NilStorePassthrough(t *testing.T) {
	tA := uuid.New()
	results := []repository.SearchResult{
		{SectionID: uuid.New(), TenantID: tA, Content: "hello", DocType: models.DocTypeReference},
	}
	modeByTenant := map[uuid.UUID]string{tA: models.StalenessModeHard}

	out, err := applyStalenessToSearchResults(context.Background(), nil, results, modeByTenant, false)
	require.NoError(t, err)
	require.Equal(t, "hello", out[0].Content, "nil store is an overall no-op")
	require.Empty(t, out[0].Status)
}

// With a store present, a result whose owning tenant maps to "off" (or is absent
// from the map) is left untouched — proving staleness is routed PER result
// tenant, not by a single caller-tenant mode. The nil db never gets touched
// because off/absent tenants skip staleness.Check entirely.
func TestApplyStaleness_PerTenantOffLeavesContent(t *testing.T) {
	store := staleness.NewPolicyStore(nil)
	tOff, tAbsent := uuid.New(), uuid.New()
	results := []repository.SearchResult{
		{SectionID: uuid.New(), TenantID: tOff, Content: "off-body", DocType: models.DocTypeReference},
		{SectionID: uuid.New(), TenantID: tAbsent, Content: "absent-body", DocType: models.DocTypeReference},
	}
	modeByTenant := map[uuid.UUID]string{tOff: models.StalenessModeOff} // tAbsent intentionally missing

	out, err := applyStalenessToSearchResults(context.Background(), store, results, modeByTenant, false)
	require.NoError(t, err)
	require.Equal(t, "off-body", out[0].Content, "off-mode tenant result untouched")
	require.Equal(t, "absent-body", out[1].Content, "tenant absent from map untouched")
	require.Empty(t, out[0].Status)
	require.Empty(t, out[1].Status)
}
