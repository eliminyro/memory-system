package models

import "testing"

func TestIsValidTenantType(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"personal accepted", TenantTypePersonal, true},
		{"shared accepted", TenantTypeShared, true},
		{"literal personal accepted", "personal", true},
		{"literal shared accepted", "shared", true},
		{"unknown rejected", "team", false},
		{"empty rejected", "", false},
		{"case-sensitive: Shared rejected", "Shared", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidTenantType(tc.input); got != tc.want {
				t.Fatalf("IsValidTenantType(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
