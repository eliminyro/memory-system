// Package retention holds the pure time-window math for the memory retention
// sweep. The sweep itself lives in internal/cleanup; the SQL lives in
// internal/repository. Keeping the arithmetic here makes it unit-testable
// without a database.
package retention

import "time"

// ExpiryCutoff returns the timestamp before which a section is considered
// expired for retirement: a document is an archive candidate when even its
// freshest section was last verified before this cutoff. The window is
// multiplier times the doc_type's staleness threshold (in days).
func ExpiryCutoff(now time.Time, thresholdDays, multiplier int) time.Time {
	return now.AddDate(0, 0, -thresholdDays*multiplier)
}

// DeleteCutoff returns the timestamp before which an archived document is
// hard-deleted: archived earlier than this is past the reversible grace window.
func DeleteCutoff(now time.Time, graceDays int) time.Time {
	return now.AddDate(0, 0, -graceDays)
}
