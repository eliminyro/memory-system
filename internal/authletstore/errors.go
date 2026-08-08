package authletstore

import (
	"errors"
	"strings"

	"github.com/eliminyro/authlet/pkg/storage"
	"gorm.io/gorm"
)

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

// mapGetErr normalizes a GORM Get/First error: a missing row becomes
// storage.ErrNotFound; anything else is returned unchanged. Shared by every
// single-row lookup in this package.
func mapGetErr(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return storage.ErrNotFound
	}
	return err
}

// mapCreateErr normalizes a GORM Create/Save error: a unique-constraint
// violation becomes storage.ErrAlreadyExists; anything else is returned
// unchanged.
func mapCreateErr(err error) error {
	if isUniqueViolation(err) {
		return storage.ErrAlreadyExists
	}
	return err
}
