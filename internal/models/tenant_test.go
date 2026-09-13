package models

import "testing"

func ptr(s string) *string { return &s }

func TestEffectiveSelfServicePolicy(t *testing.T) {
	tests := []struct {
		name     string
		override *string
		global   string
		want     string
	}{
		{"unset inherits open global", nil, SelfServicePolicyOpen, SelfServicePolicyOpen},
		{"unset inherits admin_only global", nil, SelfServicePolicyAdminOnly, SelfServicePolicyAdminOnly},
		{"unset with empty global falls to open", nil, "", SelfServicePolicyOpen},
		{"unset with invalid global falls to open", nil, "bogus", SelfServicePolicyOpen},
		{"override admin_only wins over open global", ptr(SelfServicePolicyAdminOnly), SelfServicePolicyOpen, SelfServicePolicyAdminOnly},
		{"override open wins over admin_only global", ptr(SelfServicePolicyOpen), SelfServicePolicyAdminOnly, SelfServicePolicyOpen},
		{"invalid override falls back to global", ptr("bogus"), SelfServicePolicyAdminOnly, SelfServicePolicyAdminOnly},
		{"invalid override + empty global falls to open", ptr("bogus"), "", SelfServicePolicyOpen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tn := Tenant{SelfServicePolicy: tc.override}
			if got := tn.EffectiveSelfServicePolicy(tc.global); got != tc.want {
				t.Fatalf("EffectiveSelfServicePolicy(%q) with override %v = %q, want %q", tc.global, tc.override, got, tc.want)
			}
		})
	}
}

func TestIsValidSelfServicePolicy(t *testing.T) {
	cases := map[string]bool{"open": true, "admin_only": true, "inherit": false, "": false, "Open": false}
	for in, want := range cases {
		if got := IsValidSelfServicePolicy(in); got != want {
			t.Fatalf("IsValidSelfServicePolicy(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNormalizeStalenessMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"off coerces to advisory", StalenessModeOff, StalenessModeAdvisory},
		{"empty coerces to advisory", "", StalenessModeAdvisory},
		{"unknown coerces to advisory", "bogus", StalenessModeAdvisory},
		{"advisory passes through", StalenessModeAdvisory, StalenessModeAdvisory},
		{"hard passes through", StalenessModeHard, StalenessModeHard},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeStalenessMode(tc.in); got != tc.want {
				t.Fatalf("NormalizeStalenessMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidStalenessModesHasNoOff(t *testing.T) {
	if _, ok := ValidStalenessModes[StalenessModeOff]; ok {
		t.Fatal("ValidStalenessModes must not contain off")
	}
	for _, m := range []string{StalenessModeAdvisory, StalenessModeHard} {
		if _, ok := ValidStalenessModes[m]; !ok {
			t.Fatalf("ValidStalenessModes must contain %q", m)
		}
	}
}

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
