package models

// ScanThreshold gates the nightly cleanup scanner (FindNearDuplicatePairs): MAX
// section-pair cosine per doc pair. The write-time store_memory guard uses the
// per-tenant/global duplicate_threshold instead (COALESCE(override, default)).
const ScanThreshold = 0.85

// InferDocType classifies a document by category/slug when doc_type wasn't set.
// Mirrors the SQL backfill rules for legacy docs so new writes land the same.
func InferDocType(category string, subcategory *string, slug string) string {
	switch category {
	case "projects":
		if slug == "state" {
			return DocTypeProjectState
		}
		// Treat design / plan / audit / backlog docs as audit-tier (30d threshold).
		for _, marker := range []string{"audit", "plan", "design", "backlog"} {
			if containsFold(slug, marker) {
				return DocTypeAudit
			}
		}
		return DocTypeReference
	case "learnings":
		return DocTypeLearning
	case "preferences":
		return DocTypePreference
	case "tools":
		return DocTypeTool
	case "journal":
		return DocTypeJournal
	case "handoffs":
		return DocTypeHandoff
	case "prompts":
		return DocTypePrompt
	default:
		return DocTypeReference
	}
}

// containsFold is a local case-insensitive substring test.
func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
