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
	// Tenant + email come from the signed custom claims; sub is the user id.
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{
		Subject: "user-id-1",
		Extra:   map[string]any{"tenant_id": tid.String(), "email": "admin@example.com"},
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
	// tenant_id claim present, no email claim — tenant must still be set.
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{
		Subject: "user-id-1",
		Extra:   map[string]any{"tenant_id": tid.String()},
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

// A claims set whose tenant_id is absent/unparseable must be a no-op passthrough
// (fail secure for legacy pre-fix tokens that lack the tenant_id claim).
func TestUserContextBridge_MissingTenantIDClaimIsNoOp(t *testing.T) {
	w := &Wiring{}

	var seenTenant uuid.UUID
	var seenSubjectOK bool
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenTenant = auth.TenantIDFromContext(r.Context())
		_, seenSubjectOK = auth.SubjectFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	// sub present but NO tenant_id claim (legacy token shape).
	ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{Subject: "user-id-1"})
	req = req.WithContext(ctx)
	w.UserContextBridge()(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seenTenant != uuid.Nil {
		t.Fatalf("missing tenant_id claim must not populate tenant id; got %s", seenTenant)
	}
	if seenSubjectOK {
		t.Fatal("missing tenant_id claim must not populate a subject (fail secure)")
	}
}
