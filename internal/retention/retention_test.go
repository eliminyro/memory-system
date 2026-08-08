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

func TestWindowSafe(t *testing.T) {
	cases := []struct {
		multiplier int
		graceDays  int
		want       bool
	}{
		{3, 30, true},
		{1, 1, true},
		{0, 30, false},
		{3, 0, false},
		{-1, 30, false},
		{3, -5, false},
		{0, 0, false},
	}
	for _, c := range cases {
		if got := WindowSafe(c.multiplier, c.graceDays); got != c.want {
			t.Errorf("WindowSafe(%d, %d) = %v, want %v", c.multiplier, c.graceDays, got, c.want)
		}
	}
}
