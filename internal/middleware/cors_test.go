package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_PublicOAuthPath_ReflectsOrigin(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"oauth-token", "/oauth/token"},
		{"oauth-authorize", "/oauth/authorize"},
		{"well-known-as", "/.well-known/oauth-authorization-server"},
		{"well-known-prm", "/.well-known/oauth-protected-resource/mcp"},
		{"well-known-oidc", "/.well-known/openid-configuration"},
		{"well-known-jwks", "/.well-known/jwks.json"},
		{"mcp-root", "/mcp"},
		{"mcp-subpath", "/mcp/anything"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Origin", "http://localhost:6274")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:6274" {
				t.Fatalf("Allow-Origin=%q want %q", got, "http://localhost:6274")
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
				t.Fatalf("Allow-Credentials must NOT be set on public OAuth surface, got %q", got)
			}
		})
	}
}

func TestCORS_NonPublicPath_NoHeaders(t *testing.T) {
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:6274")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin must be empty on non-OAuth path, got %q", got)
	}
}

func TestCORS_NoOriginHeader_NoHeaders(t *testing.T) {
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/oauth/token", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin must be empty when Origin header absent, got %q", got)
	}
}

func TestCORS_Preflight_204NoContent(t *testing.T) {
	called := false
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/oauth/token", nil)
	req.Header.Set("Origin", "http://localhost:6274")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d want %d", rec.Code, http.StatusNoContent)
	}
	if called {
		t.Fatal("preflight must not call next handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "authorization, content-type" {
		t.Fatalf("preflight should echo requested headers, got %q", got)
	}
}

func TestCORS_Preflight_DefaultsHeadersWhenNoRequestHeaders(t *testing.T) {
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/oauth/token", nil)
	req.Header.Set("Origin", "http://localhost:6274")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("default Allow-Headers=%q want %q", got, "Authorization, Content-Type")
	}
}

func TestCORS_VaryHeader(t *testing.T) {
	h := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/oauth/token", nil)
	req.Header.Set("Origin", "http://localhost:6274")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	vary := rec.Header().Values("Vary")
	if len(vary) != 2 || vary[0] != "Origin" || vary[1] != "Access-Control-Request-Headers" {
		t.Fatalf("Vary headers=%v want [Origin, Access-Control-Request-Headers]", vary)
	}
}
