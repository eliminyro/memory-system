package authz

import (
	"context"
	"testing"
)

// TestDefaultNamespace_Rewrites asserts every relation's rewrite rule matches design D1 (task 2.1).
func TestDefaultNamespace_Rewrites(t *testing.T) {
	ns := DefaultNamespace()

	tests := []struct {
		objType  string
		relation string
		want     []Rewrite
	}{
		{TypeSystem, RelAdmin, []Rewrite{thisRewrite()}},

		{TypeTenant, RelSystem, []Rewrite{thisRewrite()}},
		{TypeTenant, RelAdmin, []Rewrite{thisRewrite(), from(RelSystem, RelAdmin)}},
		{TypeTenant, RelOwner, []Rewrite{thisRewrite()}},
		{TypeTenant, RelManager, []Rewrite{thisRewrite(), computed(RelAdmin), computed(RelOwner)}},
		{TypeTenant, RelMember, []Rewrite{thisRewrite(), computed(RelManager)}},
		{TypeTenant, RelViewer, []Rewrite{thisRewrite(), computed(RelMember)}},

		{TypeDocument, RelTenant, []Rewrite{thisRewrite()}},
		{TypeDocument, RelViewer, []Rewrite{thisRewrite(), computed(RelEditor), from(RelTenant, RelViewer)}},
		{TypeDocument, RelEditor, []Rewrite{thisRewrite(), from(RelTenant, RelMember)}},
	}

	for _, tc := range tests {
		rd, ok := ns.Relation(tc.objType, tc.relation)
		if !ok {
			t.Errorf("%s#%s: not defined in namespace", tc.objType, tc.relation)
			continue
		}
		if len(rd.Rewrites) != len(tc.want) {
			t.Errorf("%s#%s: got %d rewrites, want %d (%+v)", tc.objType, tc.relation, len(rd.Rewrites), len(tc.want), rd.Rewrites)
			continue
		}
		for i, w := range tc.want {
			got := rd.Rewrites[i]
			if got != w {
				t.Errorf("%s#%s rewrite[%d]: got %+v, want %+v", tc.objType, tc.relation, i, got, w)
			}
		}
	}
}

// TestDefaultNamespace_WildcardViewerSubject confirms tenant#viewer lists user:* as a direct subject (public-read enabler).
func TestDefaultNamespace_WildcardViewerSubject(t *testing.T) {
	ns := DefaultNamespace()
	rd, ok := ns.Relation(TypeTenant, RelViewer)
	if !ok {
		t.Fatal("tenant#viewer not defined")
	}
	wantWildcard := TypeUser + ":" + Wildcard
	found := false
	for _, s := range rd.DirectSubjects {
		if s == wantWildcard {
			found = true
		}
	}
	if !found {
		t.Errorf("tenant#viewer DirectSubjects = %v, want to include %q", rd.DirectSubjects, wantWildcard)
	}
}

// TestDefaultNamespace_UnknownRelation confirms Relation reports absence.
func TestDefaultNamespace_UnknownRelation(t *testing.T) {
	ns := DefaultNamespace()
	if _, ok := ns.Relation(TypeDocument, "owner"); ok {
		t.Error("document#owner should not be defined")
	}
	if _, ok := ns.Relation("group", RelMember); ok {
		t.Error("group type should not be defined")
	}
}

// TestCheck_EditorImpliesViewer asserts the editor⇒viewer fix: a subject with a
// direct document#editor grant passes a document#viewer Check, while a subject
// with only document#viewer does NOT pass document#editor (no back-edge).
func TestCheck_EditorImpliesViewer(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	// de: direct editor grant to "writer". dv: direct viewer grant to "reader".
	mustWrite(t, store, tup(TypeDocument, "de", RelEditor, TypeUser, "writer", ""))
	mustWrite(t, store, tup(TypeDocument, "dv", RelViewer, TypeUser, "reader", ""))
	e := NewEngine(store)

	tests := []struct {
		name     string
		objID    string
		relation string
		subjID   string
		want     bool
	}{
		{"direct editor is editor", "de", RelEditor, "writer", true},
		{"direct editor is also viewer (editor⇒viewer)", "de", RelViewer, "writer", true},
		{"direct viewer is viewer", "dv", RelViewer, "reader", true},
		{"direct viewer is NOT editor (no back-edge)", "dv", RelEditor, "reader", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Check(ctx, TypeDocument, tc.objID, tc.relation, TypeUser, tc.subjID)
			if err != nil {
				t.Fatalf("Check(document:%s#%s@user:%s) error: %v", tc.objID, tc.relation, tc.subjID, err)
			}
			if got != tc.want {
				t.Errorf("Check(document:%s#%s@user:%s) = %v, want %v", tc.objID, tc.relation, tc.subjID, got, tc.want)
			}
		})
	}
}

// TestCheck_ManagerOrdering asserts admin ⊆ manager ⊆ member ⊆ viewer: a manager
// reaches member/viewer, an admin reaches manager, but a plain member does NOT
// reach manager.
func TestCheck_ManagerOrdering(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	mustWrite(t, store, tup(TypeTenant, "tm", RelManager, TypeUser, "mgr", ""))
	mustWrite(t, store, tup(TypeTenant, "tm", RelMember, TypeUser, "mem", ""))
	mustWrite(t, store, tup(TypeTenant, "tm", RelAdmin, TypeUser, "adm", ""))
	e := NewEngine(store)

	tests := []struct {
		name     string
		relation string
		subjID   string
		want     bool
	}{
		{"manager is member", RelMember, "mgr", true},
		{"manager is viewer", RelViewer, "mgr", true},
		{"manager is manager (direct)", RelManager, "mgr", true},
		{"member is NOT manager", RelManager, "mem", false},
		{"member is member (direct)", RelMember, "mem", true},
		{"admin is manager (admin⇒manager)", RelManager, "adm", true},
		{"admin is member", RelMember, "adm", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Check(ctx, TypeTenant, "tm", tc.relation, TypeUser, tc.subjID)
			if err != nil {
				t.Fatalf("Check(tenant:tm#%s@user:%s) error: %v", tc.relation, tc.subjID, err)
			}
			if got != tc.want {
				t.Errorf("Check(tenant:tm#%s@user:%s) = %v, want %v", tc.relation, tc.subjID, got, tc.want)
			}
		})
	}
}

// TestCheck_SystemAdminResolvesDocumentViewer confirms the deepest chain — a
// system admin reading a doc via document#viewer — terminates, grants, and stays
// at the design's depth 5 (well under DefaultMaxDepth). This also demonstrates no
// rewrite cycle was introduced by the editor⇒viewer + manager edits.
func TestCheck_SystemAdminResolvesDocumentViewer(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	mustWrite(t, store, tup(TypeSystem, SystemObjectID, RelAdmin, TypeUser, "sa", ""))
	mustWrite(t, store, tup(TypeTenant, "td", RelSystem, TypeSystem, SystemObjectID, ""))
	mustWrite(t, store, tup(TypeDocument, "dd", RelTenant, TypeTenant, "td", ""))

	// Default depth: grants with no error.
	e := NewEngine(store)
	got, err := e.Check(ctx, TypeDocument, "dd", RelViewer, TypeUser, "sa")
	if err != nil {
		t.Fatalf("default-depth Check error: %v", err)
	}
	if !got {
		t.Error("system admin should resolve document#viewer, got deny")
	}

	// The chain resolves at exactly depth 5, so MaxDepth=5 still grants.
	e5 := NewEngine(store)
	e5.MaxDepth = 5
	got5, err5 := e5.Check(ctx, TypeDocument, "dd", RelViewer, TypeUser, "sa")
	if err5 != nil {
		t.Fatalf("depth-5 Check error: %v", err5)
	}
	if !got5 {
		t.Error("chain should resolve within depth 5, got deny")
	}
}

// TestCheck_OwnerFoldsIntoManagerNotAdmin asserts the personal-owner-role model:
// a tenant#owner is a full manager (owner ⇒ manager ⇒ member ⇒ viewer) but NOT an
// admin (owner ⊄ admin), a system admin still resolves admin on the same tenant via
// the system parent edge, and shared-tenant direct-admin resolution is unchanged.
func TestCheck_OwnerFoldsIntoManagerNotAdmin(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	// Personal tenant "town": a direct owner + the system parent edge so a global
	// admin reaches it. A global admin subject "sa".
	mustWrite(t, store, tup(TypeTenant, "town", RelOwner, TypeUser, "owner", ""))
	mustWrite(t, store, tup(TypeTenant, "town", RelSystem, TypeSystem, SystemObjectID, ""))
	mustWrite(t, store, tup(TypeSystem, SystemObjectID, RelAdmin, TypeUser, "sa", ""))
	// Shared tenant "tshared": a direct admin (resolution must be unchanged).
	mustWrite(t, store, tup(TypeTenant, "tshared", RelAdmin, TypeUser, "shadm", ""))
	e := NewEngine(store)

	tests := []struct {
		name     string
		objID    string
		relation string
		subjID   string
		want     bool
	}{
		// owner ⇒ manager/member/viewer
		{"owner is manager", "town", RelManager, "owner", true},
		{"owner is member", "town", RelMember, "owner", true},
		{"owner is viewer", "town", RelViewer, "owner", true},
		// owner ⊄ admin — the security boundary
		{"owner is NOT admin", "town", RelAdmin, "owner", false},
		// system admin still administers a personal tenant (admin from system)
		{"system admin is admin of personal tenant", "town", RelAdmin, "sa", true},
		{"system admin is manager of personal tenant", "town", RelManager, "sa", true},
		// owner has no reach into any other tenant
		{"owner is not viewer of a foreign tenant", "tshared", RelViewer, "owner", false},
		{"stranger is not viewer of town", "town", RelViewer, "nobody", false},
		// shared-tenant direct admin resolution unchanged
		{"shared admin is admin", "tshared", RelAdmin, "shadm", true},
		{"shared admin is manager", "tshared", RelManager, "shadm", true},
		{"shared admin is member", "tshared", RelMember, "shadm", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Check(ctx, TypeTenant, tc.objID, tc.relation, TypeUser, tc.subjID)
			if err != nil {
				t.Fatalf("Check(tenant:%s#%s@user:%s) error: %v", tc.objID, tc.relation, tc.subjID, err)
			}
			if got != tc.want {
				t.Errorf("Check(tenant:%s#%s@user:%s) = %v, want %v", tc.objID, tc.relation, tc.subjID, got, tc.want)
			}
		})
	}
}

// mustWrite writes a tuple to the store, failing the test on error.
func mustWrite(t *testing.T, s Store, tp Tuple) {
	t.Helper()
	if err := s.Write(context.Background(), tp); err != nil {
		t.Fatalf("write %+v: %v", tp, err)
	}
}
