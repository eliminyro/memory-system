package authletstore

import "strings"

// isUniqueViolation reports whether err is a unique-constraint violation.
// String-matched so it works on both Postgres ("duplicate key...") and sqlite
// ("UNIQUE constraint failed").
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate")
}
