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

// TestIsEpisodic asserts journal is the only episodic doc_type among every
// member of ValidDocTypes — a fifth doc_type accidentally marked episodic (or
// journal not being episodic) would silently defeat the curation exemptions.
func TestIsEpisodic(t *testing.T) {
	for dt := range ValidDocTypes {
		want := dt == DocTypeJournal
		if got := IsEpisodic(dt); got != want {
			t.Errorf("IsEpisodic(%q) = %v, want %v", dt, got, want)
		}
	}
	if IsEpisodic("not_a_real_doc_type") {
		t.Error("IsEpisodic on an unknown doc_type must be false")
	}
}

func TestEpisodicDocTypes(t *testing.T) {
	set := EpisodicDocTypes()
	if len(set) != 1 || set[0] != DocTypeJournal {
		t.Errorf("EpisodicDocTypes() = %v, want [%q]", set, DocTypeJournal)
	}
}

func strPtr(s string) *string { return &s }
