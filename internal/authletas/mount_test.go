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

// newTestWiring constructs a Wiring with a real *as.AS backed by an
// in-memory authlet storage and a stand-in upstream OIDC provider. This
// avoids requiring live Google discovery (Setup performs network I/O) but
// still produces an AS whose Handler() and well-known handlers route real
// HTTP requests. BearerMW, PRMHandler, and RunCleanup get cheap stubs —
// Mount does not exercise them.
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
		Issuer:       Issuer,
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

// TestMount_RegistersAllExpectedRoutes covers the route table Mount
// installs on a stdlib ServeMux. Each path should resolve to its
// registered handler (not 404). We do not assert on response bodies — a
// 200, 400, or 405 is fine as long as the path is recognised.
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

// TestMount_PRMHandlerInvoked verifies the PRMHandler stub on Wiring
// actually fires for the well-known PRM URL — confirming Mount wires
// w.PRMHandler (not w.AS.JWKSHandler or another field) to the PRM path.
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

// TestMount_NonGETWellKnownReturns405 confirms the method restriction in
// "GET /path" patterns rejects non-GET requests (Go 1.22+ behaviour).
func TestMount_NonGETWellKnownReturns405(t *testing.T) {
	mux := http.NewServeMux()
	w := newTestWiring(t)
	w.Mount(mux)

	req := httptest.NewRequest(http.MethodPost, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
