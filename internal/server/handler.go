package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gorm.io/gorm"

	"github.com/eliminyro/memory-system/internal/auth"
	"github.com/eliminyro/memory-system/internal/authletas"
	"github.com/eliminyro/memory-system/internal/mcp"
	"github.com/eliminyro/memory-system/internal/middleware"
	"github.com/eliminyro/memory-system/internal/service"
	"github.com/eliminyro/memory-system/internal/version"
)

// Deps is the dependency bundle for NewHandler; callers build it, NewHandler wires it.
type Deps struct {
	DB            *gorm.DB
	MCPServer     *mcp.Server
	KeyValidator  *auth.APIKeyValidator
	AuthletWiring *authletas.Wiring // optional; nil = API-key-only path
	Memory        *service.MemoryService
	UIClientID    string

	// ImportJobs backs the admin async-import surface; ImportMaxUploadBytes caps
	// each uploaded archive (config.ImportMaxUploadBytes).
	ImportJobs           importJobStore
	ImportMaxUploadBytes int64

	// PublicBaseURL is the server's external origin (scheme+host, no trailing
	// slash); the UI's OAuth config is derived from it so login works on any host.
	PublicBaseURL string

	// HTTP hardening. MaxRequestBytes caps every request body (<= 0 disables).
	// RateLimitRPS <= 0 disables the throttle — the zero value leaves tests and
	// the API-key-only path unthrottled.
	MaxRequestBytes int64
	RateLimitRPS    float64
	RateLimitBurst  int
}

// NewHandler returns the full HTTP handler (mux + middleware) for the server.
func NewHandler(d Deps) http.Handler {
	mux := http.NewServeMux()

	apiKeyMW := auth.APIKeyMiddleware(d.KeyValidator)

	// MCP endpoints. With authlet wiring, JWT-shaped Bearer tokens route through
	// authlet's bearer middleware; everything else falls back to API-key auth.
	// WWWAuth401 wraps both so 401s always carry a WWW-Authenticate challenge.
	// UserContextBridge maps JWT claims to the same context shape as the API-key path.
	mcpHandler := d.MCPServer.HTTPHandler()
	if d.AuthletWiring != nil {
		mcpHandler = d.AuthletWiring.UserContextBridge()(mcpHandler)
		mcpHandler = d.AuthletWiring.DualAuth(apiKeyMW)(mcpHandler)
		mcpHandler = d.AuthletWiring.WWWAuth401()(mcpHandler)
		d.AuthletWiring.Mount(mux)
	} else {
		mcpHandler = apiKeyMW(mcpHandler)
	}
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	// Web UI JSON API — JWT-only, reusing /mcp's bearer validation + tenant bridge.
	// Requires authlet wiring; without it the UI can't authenticate.
	if d.AuthletWiring != nil && d.Memory != nil {
		api := &apiHandler{memory: d.Memory, importJobs: d.ImportJobs, maxUploadBytes: d.ImportMaxUploadBytes}
		apiStack := d.AuthletWiring.BearerMW(d.AuthletWiring.UserContextBridge()(http.StripPrefix("/api", api.mux())))
		mux.Handle("/api/", apiStack)
	}

	// Bootstrap: pre-auth, one-shot, token-gated first-run provisioning (design
	// D3), on the root mux, deliberately outside the BearerMW-wrapped /api/
	// subtree above — no admin (and so no valid bearer token) can exist yet to
	// authenticate this call. Registered as both the exact "/bootstrap" pattern
	// (so GET/POST /bootstrap resolve directly, no redirect) and the "/bootstrap/"
	// subtree (so GET /bootstrap/config.json resolves too) — same handler either
	// way, since it is itself a small mux. The front guard (HasAnyAdmin -> 404)
	// and the token compare + one-shot guard live inside bootstrapGate /
	// service.Bootstrap; nil Memory (e.g. minimal test Deps) simply leaves the
	// route unregistered.
	if d.Memory != nil {
		bootstrapSurface := bootstrapRoutes(d.Memory, d.Memory.HasAnyAdmin, d.AuthletWiring != nil)
		mux.Handle("/bootstrap", bootstrapSurface)
		mux.Handle("/bootstrap/", bootstrapSurface)
	}

	// Web UI static shell — public, no auth; it carries no data. Only the /api
	// routes that return data require a token. GET /ui is gated (design D5):
	// 404 while un-bootstrapped, the static no-oauth page once bootstrapped but
	// OAuth is not configured, and the normal shell redirect once both hold.
	// AuthletWiring != nil is the same OAuth-configured signal used above to
	// mount the /api bearer stack.
	uiFiles, uiConfig := uiHandlers(d.UIClientID, d.PublicBaseURL)
	mux.HandleFunc("GET /ui/config.json", uiConfig)
	mux.HandleFunc("GET /ui", uiGateHandler(uiHasAdminFunc(d.Memory), d.AuthletWiring != nil))
	mux.Handle("GET /ui/", uiFiles)

	// Operational endpoints under /~/ (liveness, readiness, version). Unauthenticated
	// — probes and version checks must work without credentials.
	mux.HandleFunc("/~/health", healthHandler)
	mux.HandleFunc("/~/ready", readyHandler(d.DB))
	mux.HandleFunc("/~/version", versionHandler)

	// Middleware stack (innermost first): CORS nearest the mux; body cap and rate
	// limiter wrap the whole surface (limiter exempts the probes internally;
	// body cap exempts POST /api/admin/import, see withGlobalBodyCap);
	// SecurityHeaders outermost so even 429/413/OPTIONS responses carry the CSP.
	handler := middleware.CORS(mux)
	handler = withGlobalBodyCap(handler, d.MaxRequestBytes)
	if d.RateLimitRPS > 0 {
		handler = middleware.RateLimit(d.RateLimitRPS, d.RateLimitBurst)(handler)
	}
	handler = middleware.SecurityHeaders(handler)
	return handler
}

// importUploadPath is the sole route exempted from the global body-size cap
// (see bypassGlobalBodyCap).
const importUploadPath = "/api/admin/import"

// bypassGlobalBodyCap reports whether a request should skip the global
// MaxRequestBytes wrapper and rely solely on its own, purpose-built body-size
// enforcement. Only POST /api/admin/import qualifies: MaxRequestBytes is DoS
// hardening for the general surface, including unauthenticated routes (health
// probes, UI shell, bootstrap); this route, by contrast, is admin-authenticated
// and already enforces its own larger ImportMaxUploadBytes ceiling via
// http.MaxBytesReader inside enqueueImport. Wrapping it in the smaller global
// cap too would nest MaxBytesReaders, silently shrinking the effective limit to
// min(MaxRequestBytes, ImportMaxUploadBytes) instead of the intended, larger
// upload ceiling — so it is exempted here and governed solely by its own cap.
func bypassGlobalBodyCap(method, path string) bool {
	return method == http.MethodPost && path == importUploadPath
}

// withGlobalBodyCap wraps next with the global MaxRequestBytes cap, except for
// requests matching bypassGlobalBodyCap, which reach next unwrapped and so are
// governed solely by their own body-size enforcement. maxRequestBytes <= 0
// disables the cap entirely (next is returned as-is). Factored out of
// NewHandler so the exemption is unit-testable without standing up the full
// auth stack (see handler_test.go).
func withGlobalBodyCap(next http.Handler, maxRequestBytes int64) http.Handler {
	if maxRequestBytes <= 0 {
		return next
	}
	capped := middleware.MaxBytes(maxRequestBytes)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bypassGlobalBodyCap(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		capped.ServeHTTP(w, r)
	})
}

// uiRedirectHandler redirects /ui to /ui/, preserving the query string. authlet
// returns the OAuth code/state to redirect_uri "/ui"; dropping ?code would loop login.
func uiRedirectHandler(w http.ResponseWriter, r *http.Request) {
	target := "/ui/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readyHandler exposes db ping status. Driver errors are logged but kept out of
// the response body — they can leak connection-string fragments or credentials.
func readyHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sqlDB, err := db.DB()
		if err != nil {
			slog.Error("readiness: db handle unavailable", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unavailable"))
			return
		}
		if err := sqlDB.PingContext(r.Context()); err != nil {
			slog.Error("readiness: db ping failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}
}

// versionHandler returns the server version and build commit as JSON.
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}
