package config

import (
	"strings"
	"testing"
)

func TestParseTenantDefaults(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    TenantDefaults
		wantErr string
	}{
		{
			name:  "empty string yields safe baseline",
			input: "",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "all three set",
			input: "staleness=hard,duplicate_guard=true,cleanup_scan_enabled=true",
			want:  TenantDefaults{StalenessMode: "hard", DuplicateGuard: true, CleanupScanEnabled: true},
		},
		{
			name:  "partial: just staleness",
			input: "staleness=advisory",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "partial: just duplicate_guard",
			input: "duplicate_guard=true",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "whitespace tolerated around tokens",
			input: "  staleness = advisory ,  duplicate_guard = true ",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "staleness value is case-insensitive",
			input: "staleness=ADVISORY",
			want:  TenantDefaults{StalenessMode: "advisory", DuplicateGuard: false, CleanupScanEnabled: false},
		},
		{
			name:  "bool accepts true/false case-insensitive",
			input: "duplicate_guard=TRUE,cleanup_scan_enabled=False",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:  "bool accepts 1 and 0",
			input: "duplicate_guard=1,cleanup_scan_enabled=0",
			want:  TenantDefaults{StalenessMode: "off", DuplicateGuard: true, CleanupScanEnabled: false},
		},
		{
			name:    "unknown key fails",
			input:   "foo=bar",
			wantErr: `unknown key "foo"`,
		},
		{
			name:    "invalid staleness value fails",
			input:   "staleness=loud",
			wantErr: `invalid staleness value "loud"`,
		},
		{
			name:    "invalid bool value fails",
			input:   "duplicate_guard=maybe",
			wantErr: `invalid duplicate_guard value "maybe"`,
		},
		{
			name:    "missing equals fails",
			input:   "staleness",
			wantErr: `expected key=value`,
		},
		{
			name:    "empty key fails",
			input:   "=off",
			wantErr: `expected key=value`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTenantDefaults(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultTenantDefaults(t *testing.T) {
	got := DefaultTenantDefaults()
	want := TenantDefaults{StalenessMode: "off", DuplicateGuard: false, CleanupScanEnabled: false}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
