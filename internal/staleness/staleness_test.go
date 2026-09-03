package staleness

import (
	"testing"
	"time"

	"github.com/eliminyro/memory-system/internal/models"
)

// storeWith builds a PolicyStore serving a single doc_type's effective policy.
func storeWith(docType string, verification, expiration int) *PolicyStore {
	return NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		docType: {VerificationAgeDays: verification, ExpirationAgeDays: expiration},
	})
}

func aged(days int) models.Section {
	t := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return models.Section{VerifiedAt: &t}
}

func TestCheck_Tiering(t *testing.T) {
	const dt = models.DocTypeLearning
	cases := []struct {
		name                   string
		verification, expira   int
		ageDays                int
		mode                   string
		wantStale, wantExpired bool
	}{
		{"fresh", 30, 60, 5, models.StalenessModeHard, false, false},
		{"stale not expired (hard)", 30, 60, 45, models.StalenessModeHard, true, false},
		{"expired (hard)", 30, 60, 90, models.StalenessModeHard, true, true},
		{"advisory ignores expiration", 30, 60, 90, models.StalenessModeAdvisory, true, false},
		{"verification 0 disables nudge", 0, 60, 90, models.StalenessModeHard, false, true},
		{"expiration 0 disables withhold", 30, 0, 90, models.StalenessModeHard, true, false},
		{"both 0 never flags", 0, 0, 400, models.StalenessModeHard, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWith(dt, tc.verification, tc.expira)
			got := Check(store, aged(tc.ageDays), dt, tc.mode)
			if got.Stale != tc.wantStale {
				t.Errorf("Stale = %v, want %v", got.Stale, tc.wantStale)
			}
			if got.Expired != tc.wantExpired {
				t.Errorf("Expired = %v, want %v", got.Expired, tc.wantExpired)
			}
			if got.VerificationDays != tc.verification || got.ExpirationDays != tc.expira {
				t.Errorf("threshold days = (%d,%d), want (%d,%d)", got.VerificationDays, got.ExpirationDays, tc.verification, tc.expira)
			}
		})
	}
}

// TestCheck_NeverVerifiedUsesCreatedAt confirms the clock falls back to CreatedAt.
func TestCheck_NeverVerifiedUsesCreatedAt(t *testing.T) {
	store := storeWith(models.DocTypeLearning, 30, 0)
	sec := models.Section{CreatedAt: time.Now().Add(-45 * 24 * time.Hour)}
	if got := Check(store, sec, models.DocTypeLearning, models.StalenessModeHard); !got.Stale {
		t.Error("a never-verified section older than the verification age must be stale")
	}
}

// TestDaysByDocType_UsesVerificationAge asserts the ranking-penalty map is keyed
// to the verification age, not the expiration age.
func TestDaysByDocType_UsesVerificationAge(t *testing.T) {
	store := storeWith(models.DocTypeLearning, 30, 90)
	if got := store.DaysByDocType()[models.DocTypeLearning]; got != 30 {
		t.Errorf("DaysByDocType = %d, want 30 (verification age)", got)
	}
}

func TestPreview(t *testing.T) {
	short := "short content"
	if got := Preview(short, 200); got != short {
		t.Errorf("Preview(short) = %q, want %q", got, short)
	}

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	got := Preview(string(long), 200)
	// "…" is 3 bytes in UTF-8
	if len(got) != 200+len("…") {
		t.Errorf("Preview byte length = %d, want %d (200 + ellipsis)", len(got), 200+len("…"))
	}
	if got[:200] != string(long)[:200] {
		t.Errorf("Preview did not preserve first 200 chars")
	}
}
