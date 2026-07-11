package authletas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eliminyro/authlet/pkg/jwt"
	"github.com/eliminyro/authlet/pkg/rs"
	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/google/uuid"
)

func TestUserContextBridge_SetsTenantIDFromClaims(t *testing.T) {
	w := &Wiring{}
	tid := uuid.New()

	var seenTenant uuid.UUID
	var seenEmail string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTenant = auth.TenantIDFromContext(r.Context())
		seenEmail = auth.EmailFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{
		Subject: tid.String(),
		Extra:   map[string]any{"email": "admin@example.com"},
	})
	req = req.WithContext(ctx)

	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seenTenant != tid {
		t.Fatalf("tenant id: want %s, got %s", tid, seenTenant)
	}
	if seenEmail != "admin@example.com" {
		t.Fatalf("email: want admin@example.com, got %q", seenEmail)
	}
}

func TestUserContextBridge_NoEmailExtraStillSetsTenant(t *testing.T) {
	w := &Wiring{}
	tid := uuid.New()

	var seenTenant uuid.UUID
	var seenEmail string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTenant = auth.TenantIDFromContext(r.Context())
		seenEmail = auth.EmailFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	// Claims with no Extra map (or no "email" key) — tenant must still be set.
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{
		Subject: tid.String(),
	})
	req = req.WithContext(ctx)

	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seenTenant != tid {
		t.Fatalf("tenant id: want %s, got %s", tid, seenTenant)
	}
	if seenEmail != "" {
		t.Fatalf("email: want empty, got %q", seenEmail)
	}
}

func TestUserContextBridge_NoClaimsIsNoOp(t *testing.T) {
	w := &Wiring{}

	var seenTenant uuid.UUID
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTenant = auth.TenantIDFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	// no claims on context
	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seenTenant != uuid.Nil {
		t.Fatalf("no claims must not populate tenant id; got %s", seenTenant)
	}
}

func TestUserContextBridge_SubjectNotUUIDIsNoOp(t *testing.T) {
	w := &Wiring{}

	var seenTenant uuid.UUID
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTenant = auth.TenantIDFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{Subject: "not-a-uuid"})
	req = req.WithContext(ctx)
	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seenTenant != uuid.Nil {
		t.Fatalf("non-UUID subject must not populate tenant id; got %s", seenTenant)
	}
}
