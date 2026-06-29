package retention

import (
	"testing"
	"time"
)

func TestExpiryCutoff(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	// audit threshold 30d, multiplier 3 -> 90 days back.
	got := ExpiryCutoff(now, 30, 3)
	want := now.AddDate(0, 0, -90)
	if !got.Equal(want) {
		t.Fatalf("ExpiryCutoff = %s, want %s", got, want)
	}
}

func TestDeleteCutoff(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	got := DeleteCutoff(now, 30)
	want := now.AddDate(0, 0, -30)
	if !got.Equal(want) {
		t.Fatalf("DeleteCutoff = %s, want %s", got, want)
	}
}
