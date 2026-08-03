package service

import "testing"

// TestShouldSeedAdminEmail covers the admin-email seeding decision (design D4)
// as a pure predicate — no database. An email is seeded only when it is supplied
// AND the OAuth login path is configured; every other combination is ignored.
func TestShouldSeedAdminEmail(t *testing.T) {
	cases := []struct {
		name            string
		email           string
		oauthConfigured bool
		want            bool
	}{
		{"email set + oauth configured", "admin@example.test", true, true},
		{"email set + oauth off is ignored", "admin@example.test", false, false},
		{"email omitted + oauth configured", "", true, false},
		{"email omitted + oauth off", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldSeedAdminEmail(c.email, c.oauthConfigured); got != c.want {
				t.Errorf("shouldSeedAdminEmail(%q, %v) = %v, want %v", c.email, c.oauthConfigured, got, c.want)
			}
		})
	}
}
