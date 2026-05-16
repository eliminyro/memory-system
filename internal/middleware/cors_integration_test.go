package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCORS_AssembledMux is the integration counterpart to the unit-level
// CORS tests in this package. It verifies the CORS middleware composes
// correctly with an http.ServeMux that has the same route shape as
// cmd/server/main.go's assembly: OAuth endpoints under /oauth/*, /mcp,
// and the standalone /.well-known/* metadata routes. Stub handlers stand
// in for authlet's AS so the test runs offline; the point is the
// composition (CORS wraps the mux, OPTIONS short-circuits before the
// inner handler, Vary headers propagate through to the actual response).
func TestCORS_AssembledMux(t *testing.T) {
	mux := http.NewServeMux()

	// Stand-ins shaped like the real handlers. The integration concern is
	// route resolution + CORS header propagation, not the response bodies.
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issuer":"https://example"}`))
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(CORS(mux))
	defer srv.Close()

	const origin = "http://localhost:6274"

	t.Run("preflight on /oauth/token returns 204 with CORS headers and does not reach handler", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/oauth/token", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("preflight failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("preflight status=%d want 204", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("Allow-Origin=%q want %q", got, origin)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
			t.Fatalf("Allow-Methods=%q must list POST", got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "authorization, content-type" {
			t.Fatalf("Allow-Headers=%q want %q", got, "authorization, content-type")
		}
		if got := resp.Header.Get("Access-Control-Max-Age"); got == "" {
			t.Fatal("Max-Age must be set on preflight")
		}
	})

	t.Run("actual POST on /oauth/token reaches handler and carries CORS headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/oauth/token", strings.NewReader("grant_type=refresh_token"))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("POST status=%d want 400 (from stub handler)", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("CORS must propagate on actual response: Allow-Origin=%q want %q", got, origin)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "invalid_grant") {
			t.Fatalf("handler body lost: %q", body)
		}
	})

	t.Run("preflight Vary headers list both Origin and Request-Headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/.well-known/oauth-authorization-server", nil)
		req.Header.Set("Origin", origin)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		vary := resp.Header.Values("Vary")
		if len(vary) != 2 {
			t.Fatalf("Vary headers=%v want 2 entries", vary)
		}
		if vary[0] != "Origin" || vary[1] != "Access-Control-Request-Headers" {
			t.Fatalf("Vary=%v want [Origin, Access-Control-Request-Headers]", vary)
		}
	})

	t.Run("non-OAuth path /health gets no CORS headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
		req.Header.Set("Origin", origin)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("/health must not carry Allow-Origin, got %q", got)
		}
	})

	t.Run("credentials are never allowed on public OAuth surface", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/oauth/authorize", nil)
		req.Header.Set("Origin", origin)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Fatalf("Allow-Credentials must be empty on public OAuth surface, got %q", got)
		}
	})
}
