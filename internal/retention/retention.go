// Package retention holds the pure time-window math for the retention sweep
// (sweep in internal/cleanup, SQL in internal/repository) — unit-testable without a DB.
package retention

import "time"

// ExpiryCutoff returns the cutoff before which a section counts as expired: a
// doc is an archive candidate when even its freshest section was verified before
// it. Window = multiplier × the doc_type's staleness threshold (days).
func ExpiryCutoff(now time.Time, thresholdDays, multiplier int) time.Time {
	return now.AddDate(0, 0, -thresholdDays*multiplier)
}

// DeleteCutoff returns the cutoff before which an archived doc is hard-deleted
// (archived earlier = past the reversible grace window).
func DeleteCutoff(now time.Time, graceDays int) time.Time {
	return now.AddDate(0, 0, -graceDays)
}

// WindowSafe reports whether the retention window params are safe. A non-positive
// multiplier or grace collapses the cutoffs to "now", archiving then hard-deleting
// live docs wholesale — the sweep refuses when false. Defensive last line (audit #4).
func WindowSafe(multiplier, graceDays int) bool {
	return multiplier > 0 && graceDays > 0
}
