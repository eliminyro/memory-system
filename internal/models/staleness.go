package models

// StalenessThreshold maps a doc_type to its staleness threshold in days.
// Thresholds are configurable at runtime via the staleness_thresholds table.
type StalenessThreshold struct {
	DocType string `gorm:"size:32;primaryKey" json:"doc_type"`
	Days    int    `gorm:"not null" json:"days"`
}

func (StalenessThreshold) TableName() string { return "staleness_thresholds" }

// Duplicate-similarity thresholds. Both paths use section-level cosine
// similarity (best-matching section pair across two docs / two embeddings),
// but they tune independently:
//
//   - DuplicateGuardThreshold gates the write-time store_memory check
//     (FindSimilarDocuments — incoming section embeddings vs every existing
//     section's embedding). A match means the new write substantially
//     repeats an existing section.
//
//   - ScanThreshold gates the nightly cleanup scanner
//     (FindNearDuplicatePairs — for each pair of docs in a tenant, take the
//     MAX cosine over all section-pair combinations). A match means two
//     docs share at least one near-identical section.
//
// Both knobs are kept separate so a tenant can run aggressive store-time
// dedup (low threshold) without flooding the nightly cleanup queue, or vice
// versa. ScanThreshold defaults higher because the scanner output is queue
// noise to the human reviewer; the write-time guard fires once per
// store_memory call so a slightly looser bar is tolerable.
const (
	DuplicateGuardThreshold = 0.70
	ScanThreshold           = 0.85
)

// InferDocType classifies a document by category/slug when the caller didn't
// set doc_type explicitly. Mirrors the SQL backfill rules applied to legacy
// docs on first migration so new writes land in the same buckets.
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
	default:
		return DocTypeReference
	}
}

// containsFold is a local, case-insensitive substring test — avoids importing
// strings from a constants package.
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

// DefaultStalenessThresholds is the seed set written on first migration.
// Project state decays fastest; preferences essentially never.
var DefaultStalenessThresholds = []StalenessThreshold{
	{DocType: DocTypeProjectState, Days: 14},
	{DocType: DocTypeAudit, Days: 30},
	{DocType: DocTypeLearning, Days: 180},
	{DocType: DocTypePreference, Days: 365},
	{DocType: DocTypeTool, Days: 90},
	{DocType: DocTypeReference, Days: 90},
}
