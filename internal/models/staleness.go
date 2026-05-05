package models

// StalenessThreshold maps a doc_type to its staleness threshold in days.
// Thresholds are configurable at runtime via the staleness_thresholds table.
type StalenessThreshold struct {
	DocType string `gorm:"size:32;primaryKey" json:"doc_type"`
	Days    int    `gorm:"not null" json:"days"`
}

func (StalenessThreshold) TableName() string { return "staleness_thresholds" }

// Duplicate-similarity thresholds. Two paths use cosine similarity for
// near-duplicate detection but compute it differently, so they tune
// independently:
//
//   - DuplicateGuardThreshold gates the write-time store_memory check.
//     Math: section-level — the candidate's section embeddings are compared
//     against every existing section's embedding (FindSimilarDocuments). A
//     match means the new write substantially repeats existing content.
//
//   - ScanThreshold gates the nightly cleanup scanner. Math: doc-level — each
//     doc is collapsed to AVG(section embeddings) and compared pairwise
//     (FindNearDuplicatePairs). A match means two whole docs share a similar
//     centroid. This metric is structurally lossier; boilerplate sections
//     (Overview/Architecture/Status) push averages toward each other even
//     when the meat differs, so the bar is set higher.
//
// Empirical sweep on the pe tenant (142 docs, ~10k pairwise possibilities):
// at 0.70 the doc-AVG metric flagged ~25% of all pairs (boilerplate noise);
// at 0.85 it flagged ~1%. 0.85 is the floor for the scanner; the guard is
// kept at 0.70 because section-level math at 0.70 is already discriminating.
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
