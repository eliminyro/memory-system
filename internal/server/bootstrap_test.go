package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eliminyro/memory-system/internal/authz"
	"github.com/eliminyro/memory-system/internal/service"
)

// newBootstrapUnitSvc builds a service wired only with an in-memory authz store
// and a configured bootstrap token. db/repos are nil: every path exercised in
// this file (token-gate rejections) returns from service.Bootstrap before ever
// touching the database — mirrors internal/service/bootstrap_test.go's
// newBootstrapUnitSvc. Success and already-bootstrapped paths need a real
// transaction and are covered in bootstrap_integration_test.go.
func newBootstrapUnitSvc(configuredToken string) *service.MemoryService {
	svc := service.NewMemoryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, authz.NewMemoryStore())
	svc.BootstrapToken = configuredToken
	return svc
}

func postBootstrap(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec
}

// TestBootstrapHandler_RejectsMissingToken proves the HTTP front-end forwards
// an empty token to service.Bootstrap and maps ErrBootstrapForbidden to 403,
// without needing a database (the gate fails closed before any DB access).
func TestBootstrapHandler_RejectsMissingToken(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapHandler(svc)

	rec := postBootstrap(h, `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapHandler_RejectsBadToken covers a present-but-wrong token.
func TestBootstrapHandler_RejectsBadToken(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapHandler(svc)

	rec := postBootstrap(h, `{"token":"wrong"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if out["error"] == "" {
		t.Errorf("expected non-empty error message, got %q", rec.Body.String())
	}
}

// TestBootstrapHandler_UnconfiguredTokenFailsClosed covers design D4: an unset
// BOOTSTRAP_TOKEN refuses even a caller-supplied token.
func TestBootstrapHandler_UnconfiguredTokenFailsClosed(t *testing.T) {
	svc := newBootstrapUnitSvc("")
	h := bootstrapHandler(svc)

	rec := postBootstrap(h, `{"token":"anything"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapHandler_InvalidBodyIsBadRequest guards the decode-failure path
// (malformed JSON) distinctly from the token-gate 403.
func TestBootstrapHandler_InvalidBodyIsBadRequest(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapHandler(svc)

	rec := postBootstrap(h, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapRoute_WinsOverAPISubtreePattern proves the stdlib guarantee
// design D3 leans on: a specific pattern ("POST /api/bootstrap") is matched
// over a subtree pattern ("/api/") on the same ServeMux regardless of
// registration order (Go 1.22+ ServeMux precedence rules). If this ever
// regressed, a bootstrap request would incorrectly fall into the
// BearerMW-wrapped /api/ subtree and get an auth challenge instead of running
// bootstrap.
func TestBootstrapRoute_WinsOverAPISubtreePattern(t *testing.T) {
	mux := http.NewServeMux()
	// Stand-in for the BearerMW-wrapped /api/ subtree: any request reaching
	// here would be treated as unauthenticated.
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	mux.HandleFunc("POST /api/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinct marker, unrelated to real status codes
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/bootstrap", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d — specific pattern must win over the /api/ subtree", rec.Code, http.StatusTeapot)
	}
}

// TestNewHandler_BootstrapReachableWithoutAuthlet proves the real NewHandler
// wiring registers POST /api/bootstrap on the root mux and that it is reachable
// (not 404) even when AuthletWiring is nil (so no /api/ subtree is mounted at
// all) — the bootstrap route does not depend on JWT infra being configured.
func TestNewHandler_BootstrapReachableWithoutAuthlet(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := NewHandler(Deps{Memory: svc})

	rec := postBootstrap(h, `{"token":"wrong"}`)
	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /api/bootstrap returned 404; route not registered")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestNewHandler_NoBootstrapRouteWithoutMemory documents the nil-Memory case:
// no bootstrap route is registered, so the request 404s rather than panicking.
func TestNewHandler_NoBootstrapRouteWithoutMemory(t *testing.T) {
	h := NewHandler(Deps{})
	rec := postBootstrap(h, `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no Memory service is wired", rec.Code)
	}
}

// --- UI gate (5.2) ---

// TestSetupView_ServesWhenUnbootstrapped proves /ui renders the setup view
// while HasAnyAdmin is false, using a stub — no database required.
func TestSetupView_ServesWhenUnbootstrapped(t *testing.T) {
	h := uiGateHandler(func(context.Context) (bool, error) { return false, nil })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (setup view)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bootstrap") {
		t.Errorf("expected setup view body to mention bootstrap, got %q", rec.Body.String())
	}
}

// TestSetupView_RedirectsWhenBootstrapped proves /ui falls through to the
// normal shell redirect once HasAnyAdmin is true.
func TestSetupView_RedirectsWhenBootstrapped(t *testing.T) {
	h := uiGateHandler(func(context.Context) (bool, error) { return true, nil })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (normal shell redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("Location = %q, want /ui/", loc)
	}
}

// TestSetupView_FailsOpenOnAdminCheckError proves a HasAnyAdmin error does not
// strand the instance on the setup view (which could otherwise be probed to
// learn bootstrap state) — it falls through to the normal shell redirect.
func TestSetupView_FailsOpenOnAdminCheckError(t *testing.T) {
	h := uiGateHandler(func(context.Context) (bool, error) { return false, errors.New("boom") })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (fail open to normal shell on error)", rec.Code)
	}
}

// TestSetupView_NilMemoryTreatedAsBootstrapped proves uiHasAdminFunc's nil
// adapter preserves the pre-existing plain-redirect behavior when no Memory
// service is wired.
func TestSetupView_NilMemoryTreatedAsBootstrapped(t *testing.T) {
	h := uiGateHandler(uiHasAdminFunc(nil))

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
}

// --- Setup view real form (Task 8.1) ---

// TestSetupView_RendersRealForm proves the un-bootstrapped /ui gate serves the
// real Task 8 setup form (token input + submit), not the Task 5 placeholder
// text, and wires its script from the embedded ui/ static assets.
func TestSetupView_RendersRealForm(t *testing.T) {
	h := uiGateHandler(func(context.Context) (bool, error) { return false, nil })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (setup view)", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="setup-form"`,
		`id="setup-token"`,
		`src="/ui/setup.js"`,
		"BOOTSTRAP_TOKEN",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setup view body missing %q\nbody:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
}

// TestSetupJS_PostsToBootstrapEndpoint proves the embedded ui/setup.js (the
// script the setup view loads) targets the real pre-auth POST /api/bootstrap
// endpoint. Reads the same embed.FS the static file server uses
// (uiFS, declared in ui_embed.go) rather than the filesystem directly, so this
// fails if the file is ever missing from the embed.
func TestSetupJS_PostsToBootstrapEndpoint(t *testing.T) {
	data, err := fs.ReadFile(uiFS, "ui/setup.js")
	if err != nil {
		t.Fatalf("ui/setup.js not embedded: %v", err)
	}
	script := string(data)
	if !strings.Contains(script, "/api/bootstrap") {
		t.Errorf("setup.js does not reference /api/bootstrap:\n%s", script)
	}
}

// TestUIFileServer_ServesSetupJS proves GET /ui/setup.js is actually reachable
// through the real static file server NewHandler wires up (uiHandlers), the
// same path the setup view's <script src="/ui/setup.js"> resolves against.
func TestUIFileServer_ServesSetupJS(t *testing.T) {
	fileServer, _ := uiHandlers("client-id", "https://example.org")

	rec := httptest.NewRecorder()
	fileServer.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/setup.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for GET /ui/setup.js", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("GET /ui/setup.js returned an empty body")
	}
}
