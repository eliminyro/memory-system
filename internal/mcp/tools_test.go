package mcp

import (
	"testing"

	"github.com/google/uuid"
)

// TestParseTenantOverride covers the tenant_id override parser used by every
// tool that accepts an (admin-only) tenant_id: nil/empty means "no override",
// a valid UUID parses, and garbage is rejected before it can reach the service.
func TestParseTenantOverride(t *testing.T) {
	empty := ""
	bad := "not-a-uuid"
	valid := uuid.New()
	validStr := valid.String()

	t.Run("nil -> no override", func(t *testing.T) {
		got, err := parseTenantOverride(nil)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil", got)
		}
	})

	t.Run("empty string -> no override", func(t *testing.T) {
		got, err := parseTenantOverride(&empty)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != nil {
			t.Fatalf("got = %v, want nil", got)
		}
	})

	t.Run("valid uuid parses", func(t *testing.T) {
		got, err := parseTenantOverride(&validStr)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got == nil || *got != valid {
			t.Fatalf("got = %v, want %v", got, valid)
		}
	})

	t.Run("invalid uuid rejected", func(t *testing.T) {
		got, err := parseTenantOverride(&bad)
		if err == nil {
			t.Fatal("err = nil, want error for invalid uuid")
		}
		if got != nil {
			t.Fatalf("got = %v, want nil on error", got)
		}
	})
}
