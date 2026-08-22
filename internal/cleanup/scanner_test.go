package cleanup

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
)

// retainTenant must refuse to run (returning an error before touching any
// repository or threshold store) when the retention window is unsafe. The
// Scanner here has nil repos/thresholds, so if the guard were missing the call
// would panic instead of returning cleanly.
func TestRetainTenant_RefusesUnsafeWindow(t *testing.T) {
	cases := []struct {
		name       string
		multiplier int
		graceDays  int
	}{
		{"zero multiplier", 0, 30},
		{"zero grace", 3, 0},
		{"negative multiplier", -1, 30},
		{"negative grace", 3, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Scanner{multiplier: c.multiplier, graceDays: c.graceDays}
			stats := &ScanStats{}
			err := s.retainTenant(context.Background(), models.Tenant{ID: uuid.New()}, true, stats)
			if err == nil {
				t.Fatalf("expected error refusing unsafe window, got nil")
			}
			if !strings.Contains(err.Error(), "unsafe window") {
				t.Fatalf("error = %q, want it to mention unsafe window", err.Error())
			}
			if stats.DocsArchived != 0 || stats.DocsDeleted != 0 {
				t.Fatalf("guard ran the sweep: archived=%d deleted=%d", stats.DocsArchived, stats.DocsDeleted)
			}
		})
	}
}

func TestRetentionEligible(t *testing.T) {
	other := uuid.New()
	cases := []struct {
		name   string
		tenant models.Tenant
		want   bool
	}{
		{"hard non-bootstrap", models.Tenant{ID: other, StalenessMode: models.StalenessModeHard}, true},
		{"advisory", models.Tenant{ID: other, StalenessMode: models.StalenessModeAdvisory}, false},
		{"off", models.Tenant{ID: other, StalenessMode: models.StalenessModeOff}, false},
		{"hard bootstrap excluded", models.Tenant{ID: models.BootstrapTenantID, StalenessMode: models.StalenessModeHard}, false},
	}
	for _, c := range cases {
		if got := retentionEligible(c.tenant); got != c.want {
			t.Errorf("%s: retentionEligible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAccessRetentionEligible(t *testing.T) {
	other := uuid.New()
	cases := []struct {
		name          string
		globalEnabled bool
		tenant        models.Tenant
		want          bool
	}{
		{"global on non-bootstrap", true, models.Tenant{ID: other}, true},
		{"global off", false, models.Tenant{ID: other}, false},
		{"global on independent of staleness off", true, models.Tenant{ID: other, StalenessMode: models.StalenessModeOff}, true},
		{"global on bootstrap excluded", true, models.Tenant{ID: models.BootstrapTenantID}, false},
	}
	for _, c := range cases {
		if got := accessRetentionEligible(c.globalEnabled, c.tenant); got != c.want {
			t.Errorf("%s: accessRetentionEligible = %v, want %v", c.name, got, c.want)
		}
	}
}
