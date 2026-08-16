package service

import "testing"

// TestWithUsageWeight guards the wiring for the shipped-server usage-weight
// default: the option must set usageWeight (so cfg.UsageWeight reaches
// SearchParams via Search), and a service built without it must leave it 0 so
// every existing caller ranks byte-identically.
func TestWithUsageWeight(t *testing.T) {
	t.Run("option sets usageWeight", func(t *testing.T) {
		svc := NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, WithUsageWeight(2.5))
		if svc.usageWeight != 2.5 {
			t.Fatalf("usageWeight = %v, want 2.5", svc.usageWeight)
		}
	})

	t.Run("no option leaves usageWeight 0", func(t *testing.T) {
		svc := NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		if svc.usageWeight != 0 {
			t.Fatalf("usageWeight = %v, want 0", svc.usageWeight)
		}
	})
}
