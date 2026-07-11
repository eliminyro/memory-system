package authz

import "testing"

// TestDefaultNamespace_Rewrites enumerates every relation's rewrite rule and
// asserts it matches design D1 exactly (task 2.1).
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
		{TypeTenant, RelMember, []Rewrite{thisRewrite(), computed(RelAdmin)}},
		{TypeTenant, RelViewer, []Rewrite{thisRewrite(), computed(RelMember)}},

		{TypeDocument, RelTenant, []Rewrite{thisRewrite()}},
		{TypeDocument, RelViewer, []Rewrite{thisRewrite(), from(RelTenant, RelViewer)}},
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

// TestDefaultNamespace_WildcardViewerSubject confirms the tenant viewer relation
// documents user:* as an allowed direct subject (the public-read enabler).
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
