package models

// StalenessThreshold maps a doc_type to its staleness threshold in days.
// Thresholds are configurable at runtime via the staleness_thresholds table.
type StalenessThreshold struct {
	DocType string `gorm:"size:32;primaryKey" json:"doc_type"`
	Days    int    `gorm:"not null" json:"days"`
}

func (StalenessThreshold) TableName() string { return "staleness_thresholds" }

// DuplicateThreshold is the cosine-similarity bar at which two sections or
// documents are considered near-duplicates. Used by both the write-time
// duplicate guard and the nightly cleanup scanner; keep one canonical value
// so the two paths can't drift.
const DuplicateThreshold = 0.70

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
