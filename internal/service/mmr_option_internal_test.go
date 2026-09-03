package service

import "testing"

// TestWithMMRLambda guards the wiring for the shipped-server MMR default: the
// option must set mmrLambda, and a service built without it must leave MMR off
// (nil) so every existing caller (harness, tests) is unaffected.
func TestWithMMRLambda(t *testing.T) {
	t.Run("option sets mmrLambda", func(t *testing.T) {
		svc := NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, WithMMRLambda(0.9))
		if svc.mmrLambda == nil {
			t.Fatal("mmrLambda = nil, want non-nil")
		}
		if *svc.mmrLambda != 0.9 {
			t.Fatalf("mmrLambda = %v, want 0.9", *svc.mmrLambda)
		}
	})

	t.Run("no option leaves mmrLambda nil", func(t *testing.T) {
		svc := NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		if svc.mmrLambda != nil {
			t.Fatalf("mmrLambda = %v, want nil", *svc.mmrLambda)
		}
	})
}
