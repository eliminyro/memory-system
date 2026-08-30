package repository

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsUniqueViolation guards the B10 race fix: Upsert relies on this to treat a
// concurrent peer's insert (SQLSTATE 23505 from the partial unique index) as a
// no-op instead of an error. gorm is not configured with TranslateError, so the
// check unwraps to the pgx driver error.
func TestIsUniqueViolation(t *testing.T) {
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Error("23505 should be a unique violation")
	}
	if !isUniqueViolation(fmt.Errorf("insert cleanup row: %w", &pgconn.PgError{Code: "23505"})) {
		t.Error("wrapped 23505 should be detected via errors.As")
	}
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Error("23503 (FK violation) is not a unique violation")
	}
	if isUniqueViolation(errors.New("some other error")) {
		t.Error("a plain error is not a unique violation")
	}
	if isUniqueViolation(nil) {
		t.Error("nil is not a unique violation")
	}
}

// TestNormalizePair — Upsert relies on NormalizePair so (a,b) and (b,a) map to the
// same row. If it breaks, the queue grows two rows per pair and never dedups.
func TestNormalizePair(t *testing.T) {
	a := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	loA, hiA := NormalizePair(a, b)
	loB, hiB := NormalizePair(b, a)

	if loA != a || hiA != b {
		t.Errorf("NormalizePair(a,b) = (%s,%s), want (%s,%s)", loA, hiA, a, b)
	}
	if loB != a || hiB != b {
		t.Errorf("NormalizePair(b,a) = (%s,%s), want (%s,%s) — ordering not invariant", loB, hiB, a, b)
	}
	if loA != loB || hiA != hiB {
		t.Errorf("NormalizePair is not commutative: (a,b)=(%s,%s), (b,a)=(%s,%s)", loA, hiA, loB, hiB)
	}
}

// TestNormalizePair_Equal — defensive: equal UUIDs (never in practice) are
// returned unchanged rather than panicking.
func TestNormalizePair_Equal(t *testing.T) {
	a := uuid.New()
	lo, hi := NormalizePair(a, a)
	if lo != a || hi != a {
		t.Errorf("NormalizePair(a,a) = (%s,%s), want (%s,%s)", lo, hi, a, a)
	}
}
