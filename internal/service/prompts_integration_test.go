//go:build integration

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/authzseed"
	apperr "github.com/eliminyro/memory-system/internal/errors"
	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

func promptContent(body string) string { return "# T\n\n## H\n" + body }

// TestPrompts_CurationAndSearch: prompt writes are never duplicate-blocked, and
// prompts are absent from unfiltered search but returned when filtered.
func TestPrompts_CurationAndSearch(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)
	tok := "zebraprompt"

	_, err := f.svc.StoreDocument(ctx, "prompts", strp("derpy"), "persona", promptContent(tok+" one"), false, "", nil, nil)
	require.NoError(t, err)
	// Near-identical second prompt: not blocked (prompt duplicate_guard is false).
	res, err := f.svc.StoreDocument(ctx, "prompts", strp("derpy"), "no-slop", promptContent(tok+" one"), false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)

	unfiltered, err := f.svc.Search(ctx, tok, nil, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	for _, r := range unfiltered {
		require.NotEqual(t, models.DocTypePrompt, r.DocType, "prompt absent from unfiltered search")
	}
	cat := "prompts"
	filtered, err := f.svc.Search(ctx, tok, &cat, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	var seen bool
	for _, r := range filtered {
		if r.DocType == models.DocTypePrompt {
			seen = true
		}
	}
	require.True(t, seen, "a category-filtered search returns prompts")
}

// TestPrompts_SharedReads: prompts follow the normal readable scope. A granted
// tenant's prompt and a common-pool prompt are readable on every read path; a
// prompt in a tenant the caller cannot read stays invisible.
func TestPrompts_SharedReads(t *testing.T) {
	f := newAuthzFixture(t)
	// subjA can read tenant B (member grant); subjT (tenant T) has no grant on B.
	require.NoError(t, f.store.Write(context.Background(), authzseed.TenantMember(f.tenantB, f.subjA)))

	bCtx := ctxFor(f.tenantB, f.subjB)
	_, err := f.svc.StoreDocument(bCtx, "prompts", strp("team"), "root", promptContent("team-marker"), false, "", nil, nil)
	require.NoError(t, err)
	adminCtx := ctxFor(f.tenantA, f.admin)
	_, err = f.svc.StoreDocument(adminCtx, "prompts", strp("org"), "root", promptContent("org-marker"), false, "", &models.BootstrapTenantID, nil)
	require.NoError(t, err)

	aCtx := ctxFor(f.tenantA, f.subjA)

	// get_document: A reads B's team prompt (grant) and the org prompt (common pool).
	team, err := f.svc.GetDocument(aCtx, "prompts", strp("team"), "root", false, "", nil)
	require.NoError(t, err)
	require.Equal(t, f.tenantB, team.TenantID)
	org, err := f.svc.GetDocument(aCtx, "prompts", strp("org"), "root", false, "", nil)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapTenantID, org.TenantID)

	// list_documents (category=prompts): both shared prompts are visible to A.
	cat := "prompts"
	docs, err := f.svc.ListDocuments(aCtx, &cat, nil, nil, service.ListOptions{})
	require.NoError(t, err)
	var sawTeam, sawOrg bool
	for _, d := range docs {
		if d.DocType != models.DocTypePrompt {
			continue
		}
		switch d.TenantID {
		case f.tenantB:
			sawTeam = true
		case models.BootstrapTenantID:
			sawOrg = true
		}
	}
	require.True(t, sawTeam, "granted tenant's prompt appears in list")
	require.True(t, sawOrg, "common-pool prompt appears in list")

	// category-filtered search surfaces B's prompt across the readable set.
	res, err := f.svc.Search(aCtx, "team-marker", &cat, nil, nil, 20, false, "", nil, false)
	require.NoError(t, err)
	var sawTeamSearch bool
	for _, r := range res {
		if r.DocType == models.DocTypePrompt && r.TenantID == f.tenantB {
			sawTeamSearch = true
		}
	}
	require.True(t, sawTeamSearch, "filtered search surfaces the granted tenant's prompt")

	// subjT has no grant on B: B's team prompt is invisible, the common-pool prompt is not.
	tCtx := ctxFor(f.tenantT, f.subjT)
	_, err = f.svc.GetDocument(tCtx, "prompts", strp("team"), "root", false, "", nil)
	require.ErrorIs(t, err, apperr.ErrNotFound)
	_, err = f.svc.GetDocument(tCtx, "prompts", strp("org"), "root", false, "", nil)
	require.NoError(t, err)
}

// TestScope_Write covers set, preserve-on-omit, update, clear, and that a scope is
// now accepted on any doc_type (the prompt-only restriction is gone).
func TestScope_Write(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	// Set on a prompt.
	_, err := f.svc.StoreDocumentScoped(ctx, "prompts", strp("derpy"), "s1", promptContent("x"), false, "", nil, nil, strp("hilo"))
	require.NoError(t, err)
	require.Equal(t, "hilo", *scopeOf(t, f, ctx, "prompts", strp("derpy"), "s1"))

	// Omitted on re-store: preserved.
	_, err = f.svc.StoreDocument(ctx, "prompts", strp("derpy"), "s1", promptContent("y"), false, "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "hilo", *scopeOf(t, f, ctx, "prompts", strp("derpy"), "s1"))

	// Update.
	_, err = f.svc.StoreDocumentScoped(ctx, "prompts", strp("derpy"), "s1", promptContent("z"), false, "", nil, nil, strp("a1"))
	require.NoError(t, err)
	require.Equal(t, "a1", *scopeOf(t, f, ctx, "prompts", strp("derpy"), "s1"))

	// Clear (empty string).
	_, err = f.svc.StoreDocumentScoped(ctx, "prompts", strp("derpy"), "s1", promptContent("w"), false, "", nil, nil, strp(""))
	require.NoError(t, err)
	sc := scopeOf(t, f, ctx, "prompts", strp("derpy"), "s1")
	require.True(t, sc == nil || *sc == "")

	// A scope on a NON-prompt document is now accepted (was rejected).
	_, err = f.svc.StoreDocumentScoped(ctx, "learnings", nil, "l1", promptContent("x"), false, "", nil, nil, strp("hilo"))
	require.NoError(t, err)
	require.Equal(t, "hilo", *scopeOf(t, f, ctx, "learnings", nil, "l1"))
}

func scopeOf(t *testing.T, f *authzFixture, ctx context.Context, category string, subcategory *string, slug string) *string {
	t.Helper()
	view, err := f.svc.GetDocument(ctx, category, subcategory, slug, false, "", nil)
	require.NoError(t, err)
	return view.Scope
}

// TestPrompts_DeepSubcategory: a deep-path prompt stores, classifies as prompt,
// reads back by its exact path, and is found by a subcategory_prefix subtree list.
func TestPrompts_DeepSubcategory(t *testing.T) {
	f := newAuthzFixture(t)
	ctx := ctxFor(f.tenantA, f.subjA)

	_, err := f.svc.StoreDocument(ctx, "prompts", strp("a11s/platform"), "root", promptContent("deep marker"), false, "", nil, nil)
	require.NoError(t, err)
	_, err = f.svc.StoreDocument(ctx, "prompts", strp("a11s/gaming"), "root", promptContent("deep marker 2"), false, "", nil, nil)
	require.NoError(t, err)

	// Reads back by the exact deep path and classifies as a prompt.
	view, err := f.svc.GetDocument(ctx, "prompts", strp("a11s/platform"), "root", false, "", nil)
	require.NoError(t, err)
	require.Equal(t, models.DocTypePrompt, view.DocType)
	require.Equal(t, "a11s/platform", *view.Subcategory)

	// subcategory_prefix lists the whole a11s subtree.
	docs, err := f.svc.ListDocuments(ctx, strp("prompts"), nil, nil, service.ListOptions{SubcategoryPrefix: "a11s"})
	require.NoError(t, err)
	var subs []string
	for _, d := range docs {
		if d.DocType == models.DocTypePrompt {
			subs = append(subs, *d.Subcategory)
		}
	}
	require.ElementsMatch(t, []string{"a11s/platform", "a11s/gaming"}, subs)
}
