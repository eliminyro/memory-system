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
		{"prompts category", "prompts", "persona", strPtr("derpy"), DocTypePrompt},
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

// TestEpisodicRules asserts the seeded rules reproduce the curation exemptions:
// journal, handoff, and prompt have staleness off and every curation flag false;
// no other doc_type does.
func TestEpisodicRules(t *testing.T) {
	exempt := map[string]bool{DocTypeJournal: true, DocTypeHandoff: true, DocTypePrompt: true}
	for dt := range ValidDocTypes {
		p, ok := DefaultEffectivePolicies[dt]
		if !ok {
			t.Fatalf("no default policy for %q", dt)
		}
		curationOff := p.VerificationAgeDays == 0 && !p.DuplicateGuard && !p.CleanupScan && !p.LintStaleCheck
		if curationOff != exempt[dt] {
			t.Errorf("%q: curation-off=%v, want %v", dt, curationOff, exempt[dt])
		}
	}
}

// TestPrunableRule asserts handoff is never pruned while journal inherits prunable.
func TestPrunableRule(t *testing.T) {
	if DefaultEffectivePolicies[DocTypeHandoff].Prunable {
		t.Error("handoff must be prunable=false")
	}
	if !DefaultEffectivePolicies[DocTypeJournal].Prunable {
		t.Error("journal must inherit prunable=true")
	}
	if !DefaultEffectivePolicies[DocTypeReference].Prunable {
		t.Error("reference must be prunable=true")
	}
}

// TestChainPreviousRule asserts only handoff carries the chain_previous rule.
func TestChainPreviousRule(t *testing.T) {
	if DefaultEffectivePolicies[DocTypeHandoff].ChainPrevious == nil {
		t.Error("handoff must carry chain_previous")
	}
	if DefaultEffectivePolicies[DocTypeJournal].ChainPrevious != nil {
		t.Error("journal must not chain")
	}
}

func strPtr(s string) *string { return &s }
