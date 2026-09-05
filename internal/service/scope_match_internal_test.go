package service

import "testing"

// TestScopeMatches covers the hierarchical "/"-glob and multi-valued read scope:
// "**" crosses segments, "*" stays within one, tokens are opaque (no filesystem
// meaning), and matching is any-read-token against any-stored-pattern.
func TestScopeMatches(t *testing.T) {
	cases := []struct {
		name     string
		patterns string
		values   string
		want     bool
	}{
		{"doublestar crosses segments", "a11s/**", "a11s/gaming/root", true},
		{"doublestar matches zero segments", "a11s/**", "a11s", true},
		{"single star does not cross", "*/platform", "a11s/platform", true},
		{"single star will not span two segments", "*/platform", "a11s/gaming/platform", false},
		{"exact bare token", "hilo", "hilo", true},
		{"bare token is not a prefix", "hilo", "hilo/x", false},
		{"any read token matches any pattern", "a11s/** hilo", "personal a11s/platform", true},
		{"no read token matches", "a11s/**", "personal hilo", false},
		{"multi-pattern hit on the second", "a11s/platform a11s/gaming", "a11s/gaming", true},
		{"empty read scope never matches a scoped include", "a11s/**", "", false},
		{"empty pattern set never matches", "", "a11s", false},
		{"tilde is a literal, not a home dir", "~/x", "~/x", true},
		{"no home-directory expansion", "~/x", "/home/user/x", false},
		{"doublestar under a literal-tilde root", "~/**", "~/a/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopeMatches(tc.patterns, tc.values); got != tc.want {
				t.Fatalf("scopeMatches(%q, %q) = %v, want %v", tc.patterns, tc.values, got, tc.want)
			}
		})
	}
}
