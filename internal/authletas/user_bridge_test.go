package authletas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A claims set whose tenant_id is absent/unparseable/nil must be rejected with
// 401 invalid_token (never reach the handler tenant-less), so OAuth clients
// refresh or re-authorize instead of getting opaque downstream errors.
func TestUserContextBridge_MissingTenantIDClaimRejects(t *testing.T) {
	cases := map[string]map[string]any{
		"absent":      nil,
		"unparseable": {"tenant_id": "not-a-uuid"},
		"nil uuid":    {"tenant_id": uuid.Nil.String()},
		"wrong type":  {"tenant_id": 42},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			w := &Wiring{prmURL: "https://mem.example/.well-known/oauth-protected-resource/mcp"}
			called := false
			inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			ctx := context.WithValue(req.Context(), rs.ContextKey{}, jwt.Claims{Subject: "user-id-1", Extra: extra})
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()
			w.UserContextBridge()(inner).ServeHTTP(rec, req)

			if called {
				t.Fatal("handler must not run for a token without a valid tenant_id claim")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			ch := rec.Header().Get("WWW-Authenticate")
			if !strings.Contains(ch, `error="invalid_token"`) || !strings.Contains(ch, `resource_metadata="`+w.prmURL+`"`) {
				t.Fatalf("WWW-Authenticate = %q, want invalid_token challenge with resource_metadata", ch)
			}
			if !strings.Contains(rec.Body.String(), `"invalid_token"`) {
				t.Fatalf("body = %q, want invalid_token error", rec.Body.String())
			}
		})
	}
}
