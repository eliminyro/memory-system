package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type contextKey struct{}

// WithTenantID returns a new context with the given tenant ID.
func WithTenantID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// TenantIDFromContext extracts the tenant ID from the context.
// Returns uuid.Nil if not set.
func TenantIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(contextKey{}).(uuid.UUID)
	return id
}

// BearerToken extracts the Bearer token from an Authorization header.
// Returns an error if the header is missing or malformed.
func BearerToken(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	token, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || token == "" {
		return "", fmt.Errorf("invalid Authorization header format")
	}
	return token, nil
}
