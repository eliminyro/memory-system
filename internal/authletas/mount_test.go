package authletas

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eliminyro/authlet/pkg/as"
	"github.com/eliminyro/authlet/pkg/idp"
	"github.com/eliminyro/authlet/pkg/jwt"
	"github.com/eliminyro/authlet/pkg/storage/memstore"
)

// newTestWiring builds a Wiring with a real *as.AS over in-memory storage and
// a stand-in upstream (no live Google discovery), so Handler() and well-known
// handlers route real requests. BearerMW/PRMHandler/RunCleanup are stubs.
func newTestWiring(t *testing.T) *Wiring {
	t.Helper()
	mk := make([]byte, 32)
	if _, err := rand.Read(mk); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	mgr := jwt.NewManager(store.SigningKeys(), mk)
	if err := mgr.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	server, err := as.New(as.Config{
		Issuer:       "https://mem.example.test",
		PathPrefix:   PathPrefix,
		Upstream:     idp.NewForTest("https://upstream.test"),
		UserResolver: idp.UserResolverFunc(func(context.Context, idp.Claims) (string, error) { return "u", nil }),
		Storage:      store,
		KeyManager:   mgr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Wiring{
		AS:         server,
		BearerMW:   func(next http.Handler) http.Handler { return next },
		PRMHandler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		RunCleanup: func(context.Context) <-chan struct{} { ch := make(chan struct{}); close(ch); return ch },
	}
}

// TestMount_RegistersAllExpectedRoutes checks every path Mount installs
// resolves to a handler (not 404); response status is irrelevant.
func TestMount_RegistersAllExpectedRoutes(t *testing.T) {
	mux := http.NewServeMux()
	w := newTestWiring(t)
	w.Mount(mux)

	cases := []struct {
		name string
		path string
	}{
		{"oauth-authorize", PathPrefix + "/authorize"},
		{"oauth-token", PathPrefix + "/token"},
		{"oauth-register", PathPrefix + "/register"},
		{"oauth-idp-callback", PathPrefix + "/idp/callback"},
		{"metadata-as", "/.well-known/oauth-authorization-server"},
		{"metadata-oidc", "/.well-known/openid-configuration"},
		{"jwks", "/.well-known/jwks.json"},
		{"prm-mcp", "/.well-known/oauth-protected-resource/mcp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("path %q returned 404; Mount did not register it", c.path)
			}
		})
	}
}

// TestMount_PRMHandlerInvoked verifies Mount wires w.PRMHandler (not another
// field) to the well-known PRM path.
func TestMount_PRMHandlerInvoked(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	w := newTestWiring(t)
	w.PRMHandler = func(rw http.ResponseWriter, _ *http.Request) {
		called = true
		rw.Header().Set("X-PRM", "yes")
		rw.WriteHeader(http.StatusOK)
	}
	w.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !called {
		t.Fatal("PRMHandler was not invoked for /.well-known/oauth-protected-resource/mcp")
	}
	if got := rec.Header().Get("X-PRM"); got != "yes" {
		t.Fatalf("X-PRM = %q, want yes", got)
	}
}

// TestMount_NonGETWellKnownReturns405 confirms the "GET /path" patterns reject
// non-GET on every well-known path (Go 1.22+).
func TestMount_NonGETWellKnownReturns405(t *testing.T) {
	mux := http.NewServeMux()
	w := newTestWiring(t)
	w.Mount(mux)

	paths := []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
		"/.well-known/jwks.json",
		"/.well-known/oauth-protected-resource/mcp",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, p, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s: status = %d, want 405", p, rec.Code)
			}
		})
	}
}

// TestMount_OAuthWithoutTrailingSlashRedirects locks in ServeMux behaviour: a
// GET /oauth (no trailing slash) redirects to /oauth/ (a 3xx; 301 on go1.25,
// 307/308 on newer). Clients append full paths like /oauth/authorize, so bare
// /oauth is mostly human-typed.
func TestMount_OAuthWithoutTrailingSlashRedirects(t *testing.T) {
	mux := http.NewServeMux()
	w := newTestWiring(t)
	w.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, PathPrefix, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// The exact redirect code for a bare subtree path varies by Go version
	// (301 on go1.25, 307/308 on newer) — any of them redirects to /oauth/.
	switch rec.Code {
	case http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		t.Fatalf("GET %s: status = %d, want a 3xx redirect", PathPrefix, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != PathPrefix+"/" {
		t.Fatalf("Location = %q, want %q", loc, PathPrefix+"/")
	}
}
