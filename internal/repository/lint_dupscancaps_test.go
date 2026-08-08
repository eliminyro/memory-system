package repository

import "testing"

// RG3: dupScanCaps clamps each bound to [1, default]. A caller may lower a
// bound but never raise it above the package default, so a huge input can't
// blow up the O(N^2) near-duplicate scan.
func TestDupScanCapsClampsToDefault(t *testing.T) {
	cases := []struct {
		name        string
		in          LintThresholds
		wantMaxSecs int
		wantNbrs    int
		wantPairs   int
	}{
		{
			name:        "huge values clamped to defaults",
			in:          LintThresholds{DuplicateMaxSections: 10_000_000, DuplicateNeighbors: 10_000_000, DuplicateMaxPairs: 10_000_000},
			wantMaxSecs: defaultDupMaxSections,
			wantNbrs:    defaultDupNeighbors,
			wantPairs:   defaultDupMaxPairs,
		},
		{
			name:        "zero falls back to defaults",
			in:          LintThresholds{},
			wantMaxSecs: defaultDupMaxSections,
			wantNbrs:    defaultDupNeighbors,
			wantPairs:   defaultDupMaxPairs,
		},
		{
			name:        "small valid values left unchanged",
			in:          LintThresholds{DuplicateMaxSections: 10, DuplicateNeighbors: 5, DuplicateMaxPairs: 25},
			wantMaxSecs: 10,
			wantNbrs:    5,
			wantPairs:   25,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSecs, gotNbrs, gotPairs := tc.in.dupScanCaps()
			if gotSecs != tc.wantMaxSecs {
				t.Errorf("maxSections = %d, want %d", gotSecs, tc.wantMaxSecs)
			}
			if gotNbrs != tc.wantNbrs {
				t.Errorf("neighbors = %d, want %d", gotNbrs, tc.wantNbrs)
			}
			if gotPairs != tc.wantPairs {
				t.Errorf("maxPairs = %d, want %d", gotPairs, tc.wantPairs)
			}
		})
	}
}
