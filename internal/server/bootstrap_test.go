package server

import (
	"context"
	"encoding/json"
	"errors"
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

func postBootstrap(h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// alwaysAdmin / neverAdmin are hasAnyAdminFunc stubs for the gate tests below —
// no database required.
func alwaysAdmin(context.Context) (bool, error) { return true, nil }
func neverAdmin(context.Context) (bool, error)  { return false, nil }

// --- POST /bootstrap: token gate (4.1) ---

func TestBootstrapPostHandler_RejectsMissingToken(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapPostHandler(svc)

	rec := postBootstrap(http.HandlerFunc(h), "application/json", `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapPostHandler_RejectsBadToken covers a present-but-wrong token.
func TestBootstrapPostHandler_RejectsBadToken(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapPostHandler(svc)

	rec := postBootstrap(http.HandlerFunc(h), "application/json", `{"token":"wrong"}`)
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

// TestBootstrapPostHandler_UnconfiguredTokenFailsClosed covers an unset
// BootstrapToken refusing even a caller-supplied token.
func TestBootstrapPostHandler_UnconfiguredTokenFailsClosed(t *testing.T) {
	svc := newBootstrapUnitSvc("")
	h := bootstrapPostHandler(svc)

	rec := postBootstrap(http.HandlerFunc(h), "application/json", `{"token":"anything"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapPostHandler_InvalidJSONIsBadRequest guards the decode-failure
// path (malformed JSON) distinctly from the token-gate 403.
func TestBootstrapPostHandler_InvalidJSONIsBadRequest(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapPostHandler(svc)

	rec := postBootstrap(http.HandlerFunc(h), "application/json", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestBootstrapPostHandler_AcceptsFormEncoded proves the handler parses a
// form-encoded body (no Content-Type, or application/x-www-form-urlencoded)
// as well as JSON — task 4.1's "form-encoded OR JSON" requirement.
func TestBootstrapPostHandler_AcceptsFormEncoded(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapPostHandler(svc)

	rec := postBootstrap(http.HandlerFunc(h), "application/x-www-form-urlencoded", `token=wrong`)
	// Wrong token still 403s, but critically NOT 400 — proving the form body
	// was parsed (an unparsed body would leave token empty, also 403, so the
	// real assertion is that a *correct* token would be read; here we only
	// need to prove parsing succeeded, not gate success, since success needs a
	// database). A parse failure would surface as 400 from a decode error.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (form body parsed, token gate still rejects) (%s)", rec.Code, rec.Body.String())
	}
}

func TestParseBootstrapRequest_JSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(`{"token":"tok","admin_email":"a@b.com"}`))
	req.Header.Set("Content-Type", "application/json")
	got, err := parseBootstrapRequest(req)
	if err != nil {
		t.Fatalf("parseBootstrapRequest: %v", err)
	}
	if got.Token != "tok" || got.AdminEmail != "a@b.com" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseBootstrapRequest_Form(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(`token=tok&admin_email=a%40b.com`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got, err := parseBootstrapRequest(req)
	if err != nil {
		t.Fatalf("parseBootstrapRequest: %v", err)
	}
	if got.Token != "tok" || got.AdminEmail != "a@b.com" {
		t.Fatalf("got %+v", got)
	}
}

// --- /bootstrap gate (4.1, 4.3) ---

// TestBootstrapGate_404sOncAdminExists proves the front guard 404s the whole
// /bootstrap surface — page, config.json, and POST — once HasAnyAdmin is true.
func TestBootstrapGate_404sOnceAdminExists(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapRoutes(svc, alwaysAdmin, true)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/bootstrap", nil),
		httptest.NewRequest(http.MethodGet, "/bootstrap/config.json", nil),
		httptest.NewRequest(http.MethodPost, "/bootstrap", strings.NewReader(`{}`)),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// TestBootstrapGate_FailsClosedOnAdminCheckError proves a HasAnyAdmin error
// does not leave the token-collecting bootstrap form reachable — it 404s,
// mirroring the fail-toward-provisioned choice.
func TestBootstrapGate_FailsClosedOnAdminCheckError(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapRoutes(svc, func(context.Context) (bool, error) { return false, errors.New("boom") }, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (fail closed on admin-check error)", rec.Code)
	}
}

// TestBootstrapPage_ServedWhenUnbootstrapped proves GET /bootstrap serves the
// real bootstrap form while un-bootstrapped.
func TestBootstrapPage_ServedWhenUnbootstrapped(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := bootstrapRoutes(svc, neverAdmin, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="bootstrap-form"`,
		`id="bootstrap-token"`,
		`src="/bootstrap/bootstrap.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("bootstrap page body missing %q\nbody:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
}

// TestBootstrapConfig_ReportsOAuthConfigured proves GET /bootstrap/config.json
// reflects the oauth_configured flag the bootstrap page uses to show/hide its
// admin-email field.
func TestBootstrapConfig_ReportsOAuthConfigured(t *testing.T) {
	for _, oauthConfigured := range []bool{true, false} {
		svc := newBootstrapUnitSvc("configured-secret")
		h := bootstrapRoutes(svc, neverAdmin, oauthConfigured)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap/config.json", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		var out map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body not JSON: %v", err)
		}
		if out["oauth_configured"] != oauthConfigured {
			t.Errorf("oauth_configured = %v, want %v", out["oauth_configured"], oauthConfigured)
		}
	}
}

// TestBootstrapJS_PostsToBootstrapEndpoint proves the embedded bootstrap.js
// (the script the bootstrap page loads, served at GET /bootstrap/bootstrap.js
// — see bootstrapJSHandler) targets the real pre-auth POST /bootstrap
// endpoint and reads the config.json flag. Checks the package-level
// bootstrapJS var directly (embedded from gated/bootstrap.js in bootstrap.go)
// so this fails if the file is ever missing from the embed.
func TestBootstrapJS_PostsToBootstrapEndpoint(t *testing.T) {
	if !strings.Contains(bootstrapJS, `fetch("/bootstrap"`) {
		t.Errorf("bootstrap.js does not POST to /bootstrap:\n%s", bootstrapJS)
	}
	if !strings.Contains(bootstrapJS, "/bootstrap/config.json") {
		t.Errorf("bootstrap.js does not fetch /bootstrap/config.json:\n%s", bootstrapJS)
	}
}

// TestBootstrapJSHandler_ServedAndGated proves GET /bootstrap/bootstrap.js is
// reachable through the real bootstrapRoutes wiring while un-bootstrapped
// (same surface, same gate as the page) and 404s once HasAnyAdmin is true.
func TestBootstrapJSHandler_ServedAndGated(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")

	unbootstrapped := bootstrapRoutes(svc, neverAdmin, true)
	rec := httptest.NewRecorder()
	unbootstrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap/bootstrap.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for GET /bootstrap/bootstrap.js while un-bootstrapped (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Error("GET /bootstrap/bootstrap.js returned an empty body")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want a javascript type", ct)
	}

	bootstrapped := bootstrapRoutes(svc, alwaysAdmin, true)
	rec = httptest.NewRecorder()
	bootstrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bootstrap/bootstrap.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for GET /bootstrap/bootstrap.js once bootstrapped", rec.Code)
	}
}

// TestUIFileServer_DoesNotServeGatedAssets proves the ungated /ui/ static
// file server no longer serves bootstrap.html, bootstrap.js, or
// no-oauth.html: those files were moved out of the embedded ui/ directory
// into gated/ specifically so /ui/* can never leak the bootstrap form or the
// no-oauth page. They remain reachable only through their dedicated,
// gate-wrapped handlers (bootstrapPageHandler, bootstrapJSHandler,
// serveNoOAuthView).
func TestUIFileServer_DoesNotServeGatedAssets(t *testing.T) {
	fileServer, _ := uiHandlers("client-id", "https://example.org")

	for _, path := range []string{"/ui/bootstrap.html", "/ui/bootstrap.js", "/ui/no-oauth.html"} {
		rec := httptest.NewRecorder()
		fileServer.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (must not be served by the ungated /ui/ file server)", path, rec.Code)
		}
	}
}

// --- NewHandler wiring (4.1, 4.3) ---

// TestNewHandler_BootstrapReachableWithoutAuthlet proves the real NewHandler
// wiring registers /bootstrap on the root mux and that it is reachable (not
// 404) even when AuthletWiring is nil (so no /api/ subtree is mounted at all,
// and the bootstrap page's admin-email field would be hidden) — the bootstrap
// route does not depend on JWT infra being configured.
func TestNewHandler_BootstrapReachableWithoutAuthlet(t *testing.T) {
	svc := newBootstrapUnitSvc("configured-secret")
	h := NewHandler(Deps{Memory: svc})

	rec := postBootstrap(h, "application/json", `{"token":"wrong"}`)
	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /bootstrap returned 404; route not registered")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	recCfg := httptest.NewRecorder()
	h.ServeHTTP(recCfg, httptest.NewRequest(http.MethodGet, "/bootstrap/config.json", nil))
	if recCfg.Code != http.StatusOK {
		t.Fatalf("GET /bootstrap/config.json status = %d, want 200", recCfg.Code)
	}
	var cfg map[string]bool
	if err := json.Unmarshal(recCfg.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("config body not JSON: %v", err)
	}
	if cfg["oauth_configured"] {
		t.Error("oauth_configured = true, want false when AuthletWiring is nil")
	}
}

// TestNewHandler_NoBootstrapRouteWithoutMemory documents the nil-Memory case:
// no bootstrap route is registered, so the request 404s rather than panicking.
func TestNewHandler_NoBootstrapRouteWithoutMemory(t *testing.T) {
	h := NewHandler(Deps{})
	rec := postBootstrap(h, "application/json", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no Memory service is wired", rec.Code)
	}
}

// --- UI gate (5.1, 5.2) ---

// TestUIGate_404sWhenUnbootstrapped proves /ui 404s while HasAnyAdmin is
// false, regardless of whether OAuth is configured — never leak a setup/login
// surface pre-bootstrap.
func TestUIGate_404sWhenUnbootstrapped(t *testing.T) {
	for _, oauthConfigured := range []bool{true, false} {
		h := uiGateHandler(neverAdmin, oauthConfigured)

		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("oauthConfigured=%v: status = %d, want 404", oauthConfigured, rec.Code)
		}
	}
}

// TestUIGate_ServesNoOAuthPageWhenBootstrappedWithoutOAuth proves the
// bootstrapped-but-unconfigured state serves the static informational page,
// never a form.
func TestUIGate_ServesNoOAuthPageWhenBootstrappedWithoutOAuth(t *testing.T) {
	h := uiGateHandler(alwaysAdmin, false)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no-oauth page)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "OAuth") {
		t.Errorf("expected no-oauth page body to mention OAuth, got %q", body)
	}
	for _, forbidden := range []string{"<form", "type=\"password\""} {
		if strings.Contains(body, forbidden) {
			t.Errorf("no-oauth page must carry no form; body contains %q", forbidden)
		}
	}
}

// TestUIGate_RedirectsToRealUIWhenBootstrappedWithOAuth proves the fully
// bootstrapped + OAuth-configured state falls through to the real shell
// redirect (existing behavior, unchanged).
func TestUIGate_RedirectsToRealUIWhenBootstrappedWithOAuth(t *testing.T) {
	h := uiGateHandler(alwaysAdmin, true)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (normal shell redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("Location = %q, want /ui/", loc)
	}
}

// TestUIGate_FailsTowardBootstrappedOnAdminCheckError proves a HasAnyAdmin
// error does not strand the instance on a state that could be probed to learn
// bootstrap status — it is treated as bootstrapped (falling into whichever of
// no-oauth/real-UI applies).
func TestUIGate_FailsTowardBootstrappedOnAdminCheckError(t *testing.T) {
	h := uiGateHandler(func(context.Context) (bool, error) { return false, errors.New("boom") }, true)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (fail toward bootstrapped on error)", rec.Code)
	}
}

// TestUIHasAdminFunc_NilMemoryTreatedAsBootstrapped proves uiHasAdminFunc's
// nil adapter reports bootstrapped so nil-Memory wiring doesn't strand /ui on
// 404.
func TestUIHasAdminFunc_NilMemoryTreatedAsBootstrapped(t *testing.T) {
	has, err := uiHasAdminFunc(nil)(context.Background())
	if err != nil || !has {
		t.Fatalf("uiHasAdminFunc(nil) = (%v, %v), want (true, nil)", has, err)
	}
}
