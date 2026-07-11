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

type emailContextKey struct{}

// WithEmail returns a new context with the caller's email.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, emailContextKey{}, email)
}

// EmailFromContext extracts the caller's email from the context.
func EmailFromContext(ctx context.Context) string {
	email, _ := ctx.Value(emailContextKey{}).(string)
	return email
}

// SubjectTypeUser is the only subject type in the unified authorization model:
// humans and tenant service principals are both "user" subjects. It matches
// authz.TypeUser and is the value fed to the Check evaluator's subject type in
// Pass 2.
const SubjectTypeUser = "user"

// Subject is the unified authorization principal for a request. Both
// JWT-authenticated humans (subject id == tenant_users.id) and API-key callers
// (subject id == the key's subject_id, else "svc:<tenant_id>") resolve to a
// Subject. The relationship-based Check evaluator (wired in Pass 2) is
// evaluated against it. Type is always SubjectTypeUser today.
type Subject struct {
	Type string
	ID   string
}

type subjectContextKey struct{}

// WithSubject returns a new context carrying the resolved authorization
// subject.
func WithSubject(ctx context.Context, s Subject) context.Context {
	return context.WithValue(ctx, subjectContextKey{}, s)
}

// SubjectFromContext extracts the authorization subject. The bool is false when
// no subject was resolved (e.g. a JWT caller with no tenant_users row); Pass 2
// callers fail closed on that.
func SubjectFromContext(ctx context.Context) (Subject, bool) {
	s, ok := ctx.Value(subjectContextKey{}).(Subject)
	return s, ok
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
