package cleanup

import (
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
)

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
