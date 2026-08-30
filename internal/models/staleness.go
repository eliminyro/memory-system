package models

// StalenessThreshold maps a doc_type to its staleness threshold in days.
// Thresholds are configurable at runtime via the staleness_thresholds table.
type StalenessThreshold struct {
	DocType string `gorm:"size:32;primaryKey" json:"doc_type"`
	Days    int    `gorm:"not null" json:"days"`
}

func (StalenessThreshold) TableName() string { return "staleness_thresholds" }

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
	default:
		return DocTypeReference
	}
}

// episodicDocTypes marks doc_types exempt from all curation machinery
// (duplicate guard, verification withholding, lint stale-check, cleanup scan).
var episodicDocTypes = map[string]bool{
	DocTypeJournal: true,
	DocTypeHandoff: true,
}

// neverPruneDocTypes marks episodic doc_types classified as permanent —
// handoff chains are kept as project history.
var neverPruneDocTypes = map[string]bool{
	DocTypeHandoff: true,
}

// IsEpisodic reports whether a doc_type is episodic (see episodicDocTypes).
func IsEpisodic(docType string) bool {
	return episodicDocTypes[docType]
}

// IsPrunableEpisodic reports whether an episodic doc_type is classified as
// prunable. Never-prune types (handoff) are episodic but permanent.
func IsPrunableEpisodic(docType string) bool {
	return episodicDocTypes[docType] && !neverPruneDocTypes[docType]
}

// EpisodicDocTypes returns the episodic doc_type set as a slice, for binding
// as a SQL array parameter (e.g. `doc_type <> ALL(?)` / `doc_type = ANY(?)`).
func EpisodicDocTypes() []string {
	out := make([]string, 0, len(episodicDocTypes))
	for dt := range episodicDocTypes {
		out = append(out, dt)
	}
	return out
}

// PrunableEpisodicDocTypes returns episodic doc_types classified as prunable —
// episodic minus the never-prune (permanent) set.
func PrunableEpisodicDocTypes() []string {
	out := make([]string, 0, len(episodicDocTypes))
	for dt := range episodicDocTypes {
		if !neverPruneDocTypes[dt] {
			out = append(out, dt)
		}
	}
	return out
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

// DefaultStalenessThresholds is the seed set written on first migration.
// Project state decays fastest; preferences essentially never.
var DefaultStalenessThresholds = []StalenessThreshold{
	{DocType: DocTypeProjectState, Days: 14},
	{DocType: DocTypeAudit, Days: 30},
	{DocType: DocTypeLearning, Days: 180},
	{DocType: DocTypePreference, Days: 365},
	{DocType: DocTypeTool, Days: 90},
	{DocType: DocTypeReference, Days: 90},
	{DocType: DocTypeJournal, Days: 10},
	{DocType: DocTypeHandoff, Days: 3650},
}
