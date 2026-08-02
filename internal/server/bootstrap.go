package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eliminyro/memory-system/internal/service"
)

// bootstrapRequest is the POST /api/bootstrap body. Token is the only required
// field; the tenant/key fields are optional and default inside service.Bootstrap
// (BootstrapSpec) when empty. The token travels in the JSON body (not a header)
// so the same shape works from curl, the CLI-adjacent scripts, and the setup
// form (task 8) without inventing a second convention.
type bootstrapRequest struct {
	Token       string `json:"token"`
	TenantName  string `json:"tenant_name,omitempty"`
	TenantEmail string `json:"tenant_email,omitempty"`
	KeyLabel    string `json:"key_label,omitempty"`
}

// bootstrapResponse surfaces the plaintext admin key exactly once (spec: *Admin
// key is never logged*) plus enough metadata for the caller to locate the
// tenant/key it just created.
type bootstrapResponse struct {
	APIKey   string `json:"api_key"`
	TenantID string `json:"tenant_id"`
	KeyID    string `json:"key_id"`
}

// bootstrapHandler runs the pre-auth, one-shot, token-gated first-run
// provisioning (design D3). It is registered as a specific pattern on the root
// mux OUTSIDE the BearerMW-wrapped /api/ subtree — Go 1.22+ ServeMux gives a
// specific pattern ("POST /api/bootstrap") precedence over a subtree pattern
// ("/api/") regardless of registration order, so this route is reachable even
// though no admin (and therefore no valid bearer token) can exist yet. The
// constant-time token compare and one-shot guard both live inside
// service.Bootstrap; this handler is a thin caller that only maps errors to
// HTTP status.
func bootstrapHandler(memory *service.MemoryService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body bootstrapRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}

		spec := service.BootstrapSpec{
			TenantName:  body.TenantName,
			TenantEmail: body.TenantEmail,
			KeyLabel:    body.KeyLabel,
		}
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

// hasAnyAdminFunc matches service.MemoryService.HasAnyAdmin's signature. The UI
// gate depends on this narrow function type rather than *service.MemoryService
// directly so the un/bootstrapped branch is unit-testable with a stub — no
// database required (the DB-backed behavior is exercised in
// bootstrap_integration_test.go via the real service method).
type hasAnyAdminFunc func(ctx context.Context) (bool, error)

// uiHasAdminFunc adapts a possibly-nil MemoryService to hasAnyAdminFunc. A nil
// service (Deps without Memory wired) is treated as "bootstrapped" so /ui keeps
// its pre-existing plain-redirect behavior rather than getting stuck on setup.
func uiHasAdminFunc(memory *service.MemoryService) hasAnyAdminFunc {
	if memory == nil {
		return func(context.Context) (bool, error) { return true, nil }
	}
	return memory.HasAnyAdmin
}

// uiGateHandler serves the setup view while un-bootstrapped and the normal SPA
// shell (via the existing uiRedirectHandler) once an admin exists (design D1:
// bootstrapped ≡ HasAnyAdmin). A HasAnyAdmin error is logged and treated as
// bootstrapped (fails open to the normal shell) — a transient authz-store
// hiccup should not start handing out the token-collecting setup form on an
// instance that may already be provisioned.
func uiGateHandler(hasAdmin hasAnyAdminFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		has, err := hasAdmin(r.Context())
		if err != nil {
			slog.Error("ui: HasAnyAdmin check failed; serving normal shell", "error", err)
			uiRedirectHandler(w, r)
			return
		}
		if !has {
			serveSetupView(w, r)
			return
		}
		uiRedirectHandler(w, r)
	}
}

// setupHTML is the real Task 8 setup view: a standalone, dependency-light
// page (token + optional tenant name/admin email/key label -> POST
// /api/bootstrap, showing the returned admin key exactly once). It ships as
// its own file under ui/ (styled via the shared style.css, scripted via its
// own setup.js — no vendor libs, since there is no untrusted markdown to
// render pre-auth) rather than a Go string literal, so the same markup is
// also directly reachable at GET /ui/setup.html through the ordinary static
// file server for local inspection/debugging.
//
//go:embed ui/setup.html
var setupHTML string

// serveSetupView renders the setup page.
func serveSetupView(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(setupHTML))
}
