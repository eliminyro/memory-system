package repository

import (
	"testing"

	"github.com/google/uuid"
)

// TestNormalizePair — Upsert relies on NormalizePair to ensure that (a,b) and
// (b,a) map to the same row. If this breaks, the cleanup queue grows two rows
// per pair and the nightly scanner never dedups.
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

// TestNormalizePair_Equal — defensive: if both UUIDs are equal (should never
// happen in practice), NormalizePair returns them unchanged rather than
// panicking.
func TestNormalizePair_Equal(t *testing.T) {
	a := uuid.New()
	lo, hi := NormalizePair(a, a)
	if lo != a || hi != a {
		t.Errorf("NormalizePair(a,a) = (%s,%s), want (%s,%s)", lo, hi, a, a)
	}
}
