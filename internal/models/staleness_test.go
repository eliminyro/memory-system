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

func strPtr(s string) *string { return &s }
