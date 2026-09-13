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

// TestPrunableRule asserts knowledge is never pruned while journal and handoff are
// perishable with a fixed expiration age.
func TestPrunableRule(t *testing.T) {
	if DefaultEffectivePolicies[DocTypeReference].Prunable {
		t.Error("reference (knowledge base) must be prunable=false")
	}
	if DefaultEffectivePolicies[DocTypePrompt].Prunable {
		t.Error("prompt must be prunable=false")
	}
	j := DefaultEffectivePolicies[DocTypeJournal]
	if !j.Prunable || j.ExpirationAgeDays != 30 {
		t.Errorf("journal must be prunable with a 30-day expiration, got prunable=%v exp=%d", j.Prunable, j.ExpirationAgeDays)
	}
	h := DefaultEffectivePolicies[DocTypeHandoff]
	if !h.Prunable || h.ExpirationAgeDays != 90 {
		t.Errorf("handoff must be prunable with a 90-day expiration, got prunable=%v exp=%d", h.Prunable, h.ExpirationAgeDays)
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
