package errors

import "errors"

// Sentinel errors for the domain layer.
// Repos return raw DB errors; services wrap in these; handlers map to responses.
var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")
)
