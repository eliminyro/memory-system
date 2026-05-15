package authletstore

import "strings"

// isUniqueViolation returns true if err appears to be a unique-constraint
// violation. It uses a string-match so the same code works against both
// Postgres ("duplicate key value violates unique constraint") and sqlite
// ("UNIQUE constraint failed").
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate")
}
