//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

func mkIncDoc(t *testing.T, f *authzFixture, ctx context.Context, category string, sub *string, slug, body string, scope *string) uuid.UUID {
	t.Helper()
	res, err := f.svc.StoreDocumentScoped(ctx, category, sub, slug, "# T\n\n## H\n"+body, false, "", nil, nil, scope)
	require.NoError(t, err)
	require.NotNil(t, res.Document)
	return res.Document.ID
}

func linkInc(t *testing.T, f *authzFixture, ctx context.Context, src, dst uuid.UUID) {
	t.Helper()
	_, err := f.svc.CreateEdge(ctx, src, dst, models.EdgeIncludes, nil)
	require.NoError(t, err)
}

func incIDs(v *service.DocumentView) []uuid.UUID {
	out := make([]uuid.UUID, len(v.Includes))
	for i, d := range v.Includes {
		out[i] = d.ID
	}
	return out
}

func countID(ids []uuid.UUID, id uuid.UUID) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}

func manifestStatus(refs []service.IncludeRef, id uuid.UUID) string {
	for _, r := range refs {
		if r.DocumentID == id {
			return r.Status
		}
	}
	return ""
}

func anyStatus(refs []service.IncludeRef, status string) bool {
	for _, r := range refs {
		if r.Status == status {
			return true
		}
	}
	return false
}

// TestIncludes_Transitive: root -> a -> b resolves a then b after root.
func TestIncludes_Transitive(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	a := mkIncDoc(t, f, ctx, "learnings", nil, "inc-a", "aaa", nil)
	b := mkIncDoc(t, f, ctx, "learnings", nil, "inc-b", "bbb", nil)
	linkInc(t, f, ctx, root, a)
	linkInc(t, f, ctx, a, b)

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{a, b}, incIDs(v))
}

// TestIncludes_Ordering: link c before b -> c resolves before b.
func TestIncludes_Ordering(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	c := mkIncDoc(t, f, ctx, "learnings", nil, "inc-c", "ccc", nil)
	b := mkIncDoc(t, f, ctx, "learnings", nil, "inc-b", "bbb", nil)
	linkInc(t, f, ctx, root, c)
	linkInc(t, f, ctx, root, b)

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{c, b}, incIDs(v))
}

// TestIncludes_DiamondDedup: root -> {a,b}, a,b -> d. d resolves once.
func TestIncludes_DiamondDedup(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	a := mkIncDoc(t, f, ctx, "learnings", nil, "inc-a", "aaa", nil)
	b := mkIncDoc(t, f, ctx, "learnings", nil, "inc-b", "bbb", nil)
	d := mkIncDoc(t, f, ctx, "learnings", nil, "inc-d", "ddd", nil)
	linkInc(t, f, ctx, root, a)
	linkInc(t, f, ctx, root, b)
	linkInc(t, f, ctx, a, d)
	linkInc(t, f, ctx, b, d)

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, 1, countID(incIDs(v), d), "diamond resolves d exactly once")
	require.Equal(t, service.IncludeIncluded, manifestStatus(v.IncludeManifest, d))
}

// TestIncludes_CycleSafe: root -> a -> root terminates; back-edge is skipped_cycle.
func TestIncludes_CycleSafe(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	a := mkIncDoc(t, f, ctx, "learnings", nil, "inc-a", "aaa", nil)
	linkInc(t, f, ctx, root, a)
	linkInc(t, f, ctx, a, root)

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{a}, incIDs(v))
	require.Equal(t, service.IncludeSkippedCycle, manifestStatus(v.IncludeManifest, root))
}

// TestIncludes_DepthBounded: a chain deeper than the cap stops at skipped_depth.
func TestIncludes_DepthBounded(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	prev := root
	nodes := make([]uuid.UUID, 11)
	for i := 0; i < 11; i++ {
		nodes[i] = mkIncDoc(t, f, ctx, "learnings", nil, "inc-n"+string(rune('a'+i)), "n", nil)
		linkInc(t, f, ctx, prev, nodes[i])
		prev = nodes[i]
	}

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Len(t, v.Includes, 10, "depth cap bounds the resolved chain")
	require.True(t, anyStatus(v.IncludeManifest, service.IncludeSkippedDepth), "the tail is recorded skipped_depth")
	require.Equal(t, service.IncludeSkippedDepth, manifestStatus(v.IncludeManifest, nodes[10]))
}

// TestIncludes_ScopeConditional: a scoped include resolves only on a matching read
// scope; its subtree is not expanded when skipped. The scoped doc is a non-prompt.
func TestIncludes_ScopeConditional(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	u := mkIncDoc(t, f, ctx, "learnings", nil, "inc-u", "unscoped", nil)
	s := mkIncDoc(t, f, ctx, "learnings", nil, "inc-s", "scoped", strp("hilo memory-system"))
	x := mkIncDoc(t, f, ctx, "learnings", nil, "inc-x", "child-of-s", nil)
	linkInc(t, f, ctx, root, u)
	linkInc(t, f, ctx, root, s)
	linkInc(t, f, ctx, s, x)

	// Non-matching scope: s is skipped, and its child x is not expanded.
	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "a1", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{u}, incIDs(v))
	require.Equal(t, service.IncludeSkippedScope, manifestStatus(v.IncludeManifest, s))
	require.Empty(t, manifestStatus(v.IncludeManifest, x), "a skipped subtree is not walked")

	// Matching scope: s and its child resolve.
	v, err = f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "hilo", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{u, s, x}, incIDs(v))
}

// TestIncludes_ScopeHierarchicalGlob: a "**" scope resolves under a hierarchical
// read token, a multi-valued read matches if any token matches any pattern, and a
// read whose tokens match no pattern is skipped_scope.
func TestIncludes_ScopeHierarchicalGlob(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "g-root", "root", nil)
	g := mkIncDoc(t, f, ctx, "learnings", nil, "g-scoped", "scoped", strp("a11s/**"))
	linkInc(t, f, ctx, root, g)

	// "**" crosses the segment under a11s.
	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "a11s/platform", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{g}, incIDs(v))

	// Multi-valued read: one non-matching token, one matching -> resolves.
	v, err = f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "hilo a11s/gaming/root", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{g}, incIDs(v))

	// No token matches a11s/** -> skipped_scope.
	v, err = f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "hilo personal", nil)
	require.NoError(t, err)
	require.Empty(t, incIDs(v))
	require.Equal(t, service.IncludeSkippedScope, manifestStatus(v.IncludeManifest, g))
}

// TestIncludes_GrantedPromptResolves: a prompt included by a parent in a granted
// tenant resolves for the grantee (includes edges are same-tenant); a caller with
// no grant on that tenant cannot read the parent at all.
func TestIncludes_GrantedPromptResolves(t *testing.T) {
	f := newAuthzFixture(t)
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	bCtx := ctxFor(f.tenantB, f.subjB)
	parent := mkIncDoc(t, f, bCtx, "learnings", nil, "inc-parent", "parent in B", nil)
	prompt := mkIncDoc(t, f, bCtx, "prompts", strp("derpy"), "inc-secret", "SHARED-PROMPT-MARKER", nil)
	linkInc(t, f, bCtx, parent, prompt)

	// A is a member of B: the parent and its same-tenant prompt include both resolve.
	aCtx := ctxFor(f.tenantA, f.subjA)
	v, err := f.svc.GetDocumentByIDExpanded(aCtx, parent, false, "", "", nil)
	require.NoError(t, err)
	require.Contains(t, incIDs(v), prompt, "a grantee resolves the parent's prompt include")
	require.Equal(t, service.IncludeIncluded, manifestStatus(v.IncludeManifest, prompt))

	// A subject with no grant on B cannot read the parent, so its includes never load.
	tCtx := ctxFor(f.tenantT, f.subjT)
	_, err = f.svc.GetDocumentByIDExpanded(tCtx, parent, false, "", "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound)
}

// TestIncludes_ArchivedSkipped: an archived include target is skipped_missing.
func TestIncludes_ArchivedSkipped(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	a := mkIncDoc(t, f, ctx, "learnings", nil, "inc-a", "aaa", nil)
	z := mkIncDoc(t, f, ctx, "learnings", nil, "inc-z", "zzz", nil)
	linkInc(t, f, ctx, root, a)
	// Supersede archives a while keeping the includes edge.
	_, err := f.svc.CreateEdge(ctx, z, a, models.EdgeSupersedes, nil)
	require.NoError(t, err)

	v, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.NotContains(t, incIDs(v), a)
	require.Equal(t, service.IncludeSkippedMissing, manifestStatus(v.IncludeManifest, a))
}

// TestIncludes_NonExpandUnchanged: a plain read carries no includes or manifest.
func TestIncludes_NonExpandUnchanged(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "learnings", nil, "inc-root", "root", nil)
	a := mkIncDoc(t, f, ctx, "learnings", nil, "inc-a", "aaa", nil)
	linkInc(t, f, ctx, root, a)

	v, err := f.svc.GetDocument(ctx, "learnings", nil, "inc-root", false, "", nil)
	require.NoError(t, err)
	require.Nil(t, v.Includes)
	require.Nil(t, v.IncludeManifest)
}

// TestIncludes_PromptAssembly: the get_prompts replacement — a root prompt that
// includes always-apply + scoped prompt parts, assembled by read scope.
func TestIncludes_PromptAssembly(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	root := mkIncDoc(t, f, ctx, "prompts", strp("derpy"), "root", "root", nil)
	persona := mkIncDoc(t, f, ctx, "prompts", strp("derpy"), "persona", "pers", nil)
	hilo := mkIncDoc(t, f, ctx, "prompts", strp("derpy"), "hilo-rules", "hilo", strp("hilo memory-system"))
	linkInc(t, f, ctx, root, persona)
	linkInc(t, f, ctx, root, hilo)

	base, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{persona}, incIDs(base), "no scope: only always-apply parts")

	scoped, err := f.svc.GetDocumentByIDExpanded(ctx, root, false, "", "hilo", nil)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{persona, hilo}, incIDs(scoped), "matching scope adds the scoped part")
}
