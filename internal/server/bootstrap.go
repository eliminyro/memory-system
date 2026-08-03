package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"

	"github.com/eliminyro/memory-system/internal/service"
)

// bootstrapRequest is the POST /bootstrap body (design D3/D4): a token plus an
// optional admin email. Everything else service.Bootstrap needs (tenant name,
// key label) defaults server-side inside BootstrapSpec, so the pre-auth
// front-end surface stays as small as the bootstrap.html form (token + email).
type bootstrapRequest struct {
	Token      string `json:"token"`
	AdminEmail string `json:"admin_email,omitempty"`
}

// bootstrapResponse surfaces the plaintext admin key exactly once (spec: *Admin
// key is never logged*) plus enough metadata for the caller to locate the
// tenant/key it just created.
type bootstrapResponse struct {
	APIKey   string `json:"api_key"`
	TenantID string `json:"tenant_id"`
	KeyID    string `json:"key_id"`
}

// parseBootstrapRequest decodes the POST /bootstrap body as JSON when the
// Content-Type says so, and as a form-encoded body otherwise — so the same
// endpoint serves both bootstrap.js's fetch() call and a plain
// curl -d token=... -d admin_email=... invocation without inventing a second
// route.
func parseBootstrapRequest(r *http.Request) (bootstrapRequest, error) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "application/json" {
		var body bootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return bootstrapRequest{}, err
		}
		return body, nil
	}
	if err := r.ParseForm(); err != nil {
		return bootstrapRequest{}, err
	}
	return bootstrapRequest{
		Token:      r.PostFormValue("token"),
		AdminEmail: r.PostFormValue("admin_email"),
	}, nil
}

// bootstrapPostHandler runs the pre-auth, one-shot, token-gated first-run
// provisioning (design D3). It is a thin caller: the constant-time token
// compare and one-shot guard both live inside service.Bootstrap, this handler
// only decodes the request and maps errors to HTTP status.
func bootstrapPostHandler(memory *service.MemoryService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := parseBootstrapRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}

		spec := service.BootstrapSpec{AdminEmail: body.AdminEmail}
		plaintext, key, err := memory.Bootstrap(r.Context(), body.Token, spec)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrBootstrapForbidden):
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "bootstrap forbidden"})
			case errors.Is(err, service.ErrAlreadyBootstrapped):
				writeJSON(w, http.StatusConflict, map[string]string{"error": "already bootstrapped"})
			default:
				slog.Error("bootstrap: internal error", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			}
			return
		}

		writeJSON(w, http.StatusOK, bootstrapResponse{
			APIKey:   plaintext,
			TenantID: key.TenantID.String(),
			KeyID:    key.ID.String(),
		})
	}
}

// bootstrapHTML is the dedicated, self-contained /bootstrap page (design D3):
// a token field plus an admin-email field the page shows only when OAuth is
// configured (fetched from GET /bootstrap/config.json, mirroring
// ui/config.json). It ships as its own file under gated/ — NOT ui/ — so it is
// only reachable through the bootstrapGate-wrapped /bootstrap routes below,
// never through the ungated /ui/ static file server (styled via the shared
// ui/style.css, which stays public; scripted via its own bootstrap.js served
// at GET /bootstrap/bootstrap.js — no vendor libs, since there is no
// untrusted markdown to render pre-auth).
//
//go:embed gated/bootstrap.html
var bootstrapHTML string

// bootstrapJS is bootstrap.html's script, served only at GET
// /bootstrap/bootstrap.js (see bootstrapJSHandler) so it shares the page's
// gating: reachable while un-bootstrapped, 404 once HasAnyAdmin is true. It
// lives under gated/, not ui/, so the ungated /ui/ static file server never
// serves it.
//
//go:embed gated/bootstrap.js
var bootstrapJS string

// bootstrapPageHandler renders the bootstrap page.
func bootstrapPageHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(bootstrapHTML))
}

// bootstrapJSHandler serves the bootstrap page's script at
// GET /bootstrap/bootstrap.js — the only place it is reachable.
func bootstrapJSHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(bootstrapJS))
}

// bootstrapConfigHandler mirrors the existing ui/config.json pattern: a small,
// non-secret JSON blob the bootstrap page fetches to decide whether to show
// its optional admin-email field. oauth_configured is the same signal
// handler.go uses to mount the /api bearer stack (d.AuthletWiring != nil).
func bootstrapConfigHandler(oauthConfigured bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"oauth_configured": oauthConfigured})
	}
}

// hasAnyAdminFunc matches service.MemoryService.HasAnyAdmin's signature. Both
// the /bootstrap gate and the /ui gate depend on this narrow function type
// rather than *service.MemoryService directly so the un/bootstrapped branch is
// unit-testable with a stub — no database required (the DB-backed behavior is
// exercised in the integration-tagged tests).
type hasAnyAdminFunc func(ctx context.Context) (bool, error)

// uiHasAdminFunc adapts a possibly-nil MemoryService to hasAnyAdminFunc. A nil
// service (Deps without Memory wired) is treated as "bootstrapped" so /ui and
// /bootstrap don't get stuck offering setup on a minimal/test wiring that
// never provisions anything.
func uiHasAdminFunc(memory *service.MemoryService) hasAnyAdminFunc {
	if memory == nil {
		return func(context.Context) (bool, error) { return true, nil }
	}
	return memory.HasAnyAdmin
}

// bootstrapGate front-guards the whole /bootstrap surface (design D3): once
// HasAnyAdmin is true, every method and sub-route under /bootstrap (the page,
// config.json, and the POST) 404s — bootstrap is one-shot and must vanish once
// armed. A HasAnyAdmin error is logged and treated as bootstrapped (fails
// toward "assume provisioned") so a transient authz-store hiccup cannot be
// probed to learn un-bootstrapped state or re-expose the token-collecting form.
func bootstrapGate(hasAdmin hasAnyAdminFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		has, err := hasAdmin(r.Context())
		if err != nil {
			slog.Error("bootstrap: HasAnyAdmin check failed; assuming bootstrapped", "error", err)
			has = true
		}
		if has {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bootstrapRoutes builds the gated /bootstrap surface: GET serves the page,
// GET .../bootstrap.js serves the page's script, GET .../config.json serves
// the oauth_configured flag, POST provisions. All four are wrapped by
// bootstrapGate so they share one front guard.
func bootstrapRoutes(memory *service.MemoryService, hasAdmin hasAnyAdminFunc, oauthConfigured bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bootstrap", bootstrapPageHandler)
	mux.HandleFunc("GET /bootstrap/bootstrap.js", bootstrapJSHandler)
	mux.HandleFunc("GET /bootstrap/config.json", bootstrapConfigHandler(oauthConfigured))
	mux.HandleFunc("POST /bootstrap", bootstrapPostHandler(memory))
	return bootstrapGate(hasAdmin, mux)
}

// noOAuthHTML is the static, informational page served at /ui once the
// instance is bootstrapped but OAuth is not configured (design D5). It carries
// no form: there is nothing an un-configured instance can safely collect or
// submit pre-login. It lives under gated/, not ui/, so it is reachable only
// through uiGateHandler's no-oauth branch — never directly via the ungated
// /ui/ static file server.
//
//go:embed gated/no-oauth.html
var noOAuthHTML string

// serveNoOAuthView renders the no-oauth page.
func serveNoOAuthView(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(noOAuthHTML))
}

// uiGateHandler implements the three-state /ui gate (design D5, spec: *UI
// availability gating*): HasAnyAdmin false => 404 (never leak a setup or login
// surface pre-bootstrap); true + OAuth not configured => the static
// informational no-oauth page (never a form); true + OAuth configured => the
// real SPA shell redirect (existing uiRedirectHandler behavior). A
// HasAnyAdmin error is logged and treated as bootstrapped, mirroring
// bootstrapGate's fail-toward-provisioned choice, so a transient authz-store
// hiccup cannot be probed to learn un-bootstrapped state.
func uiGateHandler(hasAdmin hasAnyAdminFunc, oauthConfigured bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has, err := hasAdmin(r.Context())
		if err != nil {
			slog.Error("ui: HasAnyAdmin check failed; assuming bootstrapped", "error", err)
			has = true
		}
		if !has {
			http.NotFound(w, r)
			return
		}
		if !oauthConfigured {
			serveNoOAuthView(w, r)
			return
		}
		uiRedirectHandler(w, r)
	}
}
