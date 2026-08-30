package models

import "testing"

func TestInferDocType(t *testing.T) {
	tests := []struct {
		name        string
		category    string
		slug        string
		subcategory *string
		want        string
	}{
		{"project state doc", "projects", "state", strPtr("hilo"), DocTypeProjectState},
		{"project audit doc", "projects", "frontend-audit", strPtr("hilo"), DocTypeAudit},
		{"project plan doc", "projects", "staging-plan", strPtr("hilo"), DocTypeAudit},
		{"project design doc", "projects", "comments-design", strPtr("hilo"), DocTypeAudit},
		{"project backlog", "projects", "backlog", strPtr("hilo"), DocTypeAudit},
		{"project generic doc", "projects", "overview", strPtr("hilo"), DocTypeReference},
		{"learning doc", "learnings", "gorm", strPtr("go"), DocTypeLearning},
		{"preference doc", "preferences", "workflow", nil, DocTypePreference},
		{"tool doc", "tools", "jq", nil, DocTypeTool},
		{"unknown category", "misc", "something", nil, DocTypeReference},
		{"case-insensitive plan match", "projects", "AuditPlan", nil, DocTypeAudit},
		{"journal category", "journal", "2026-08-15", nil, DocTypeJournal},
		{"journal category any slug", "journal", "morning-notes", strPtr("work"), DocTypeJournal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InferDocType(tc.category, tc.subcategory, tc.slug)
			if got != tc.want {
				t.Errorf("InferDocType(%q, %v, %q) = %q, want %q", tc.category, tc.subcategory, tc.slug, got, tc.want)
			}
		})
	}
}

// TestIsEpisodic asserts journal and handoff are the episodic doc_types among
// every member of ValidDocTypes — a doc_type accidentally marked episodic (or one
// of these not being episodic) would silently defeat the curation exemptions.
func TestIsEpisodic(t *testing.T) {
	for dt := range ValidDocTypes {
		want := dt == DocTypeJournal || dt == DocTypeHandoff
		if got := IsEpisodic(dt); got != want {
			t.Errorf("IsEpisodic(%q) = %v, want %v", dt, got, want)
		}
	}
	if IsEpisodic("not_a_real_doc_type") {
		t.Error("IsEpisodic on an unknown doc_type must be false")
	}
}

func TestEpisodicDocTypes(t *testing.T) {
	want := map[string]bool{DocTypeJournal: true, DocTypeHandoff: true}
	set := EpisodicDocTypes()
	if len(set) != len(want) {
		t.Errorf("EpisodicDocTypes() = %v, want keys %v", set, want)
	}
	for _, dt := range set {
		if !want[dt] {
			t.Errorf("EpisodicDocTypes() returned unexpected %q", dt)
		}
	}
}

// TestIsPrunableEpisodic asserts handoff is episodic but never pruned while
// journal stays prunable, and that the retention helper binds only prunable ones.
func TestIsPrunableEpisodic(t *testing.T) {
	if !IsPrunableEpisodic(DocTypeJournal) {
		t.Error("journal must be prunable-episodic")
	}
	if IsPrunableEpisodic(DocTypeHandoff) {
		t.Error("handoff must be episodic but never pruned")
	}
	if IsPrunableEpisodic(DocTypeReference) {
		t.Error("a non-episodic doc_type is not prunable-episodic")
	}
	pruned := PrunableEpisodicDocTypes()
	if len(pruned) != 1 || pruned[0] != DocTypeJournal {
		t.Errorf("PrunableEpisodicDocTypes() = %v, want [%q]", pruned, DocTypeJournal)
	}
}

func strPtr(s string) *string { return &s }
