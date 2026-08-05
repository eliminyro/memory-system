package errors

import (
	"errors"
	"fmt"
)

// Sentinel errors for the domain layer: repos return raw DB errors, services
// wrap in these, handlers map to responses.
var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")

	// ErrInvalidRelation flags a relation name outside the accepted set for its
	// surface. It is a distinct sentinel that still wraps ErrInvalidInput (so
	// existing invalid-input handling keeps matching), letting the ACL layer be
	// the single validator while HTTP callers still map a malformed relation to
	// 400 and an authorization/ceiling denial (a bare ErrInvalidInput) to 403.
	// The "%w" wrap adds no text, so wrapped messages read exactly as before.
	ErrInvalidRelation = fmt.Errorf("%w", ErrInvalidInput)
)
