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

	// InstanceConfig + GlobalConfig back the admin global-config surface; the
	// hardening middleware reads GlobalConfig live per request, and PATCH
	// refreshes it so writes apply without a restart.
	InstanceConfig instanceConfigStore
	GlobalConfig   globalConfigAccessor

	// ConfigListener is the config-invalidation listener; /~/ready reports its
	// health when RequireConfigListener is on. Optional (nil = not reported).
	ConfigListener listenerHealth

	// SetLogLevel applies a validated slog level name to the running handler's
	// shared LevelVar, so a PATCH of log_level takes effect live.
	SetLogLevel func(level string)

	// PublicBaseURL is the server's external origin (scheme+host, no trailing
	// slash); the UI's OAuth config is derived from it so login works on any host.
	PublicBaseURL string
}

// globalConfigAccessor is the live global-config surface NewHandler threads into
// the hardening middleware (per-request reads) and the admin PATCH refresh path.
// *globalconfig.Accessor satisfies it.
type globalConfigAccessor interface {
	configRefresher
	middleware.RateLimitConfig
	MaxRequestBytes() int64
	RequireConfigListener() bool
}

// listenerHealth reports whether the config-invalidation listener holds a live
// connection; *pgnotify.Listener satisfies it.
type listenerHealth interface {
	Healthy() bool
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
		api := &apiHandler{memory: d.Memory, importJobs: d.ImportJobs, maxUploadBytes: d.ImportMaxUploadBytes, instanceConfig: d.InstanceConfig, globalCfg: d.GlobalConfig, setLogLevel: d.SetLogLevel}
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
	mux.HandleFunc("/~/ready", readyHandler(d.DB, d.GlobalConfig, d.ConfigListener))
	mux.HandleFunc("/~/version", versionHandler)

	// Middleware stack (innermost first): CORS nearest the mux; body cap + rate
	// limiter always wrap the surface, self-disabling per request off live
	// GlobalConfig; SecurityHeaders outermost so 429/413/OPTIONS carry the CSP.
	handler := middleware.CORS(mux)
	handler = withGlobalBodyCap(handler, d.GlobalConfig)
	handler = middleware.RateLimit(d.GlobalConfig)(handler)
	handler = middleware.SecurityHeaders(handler)
	return handler
}

// importUploadPath is the admin-scoped import upload route exempted from the
// global body-size cap (see bypassGlobalBodyCap); relationalImportUploadPath
// is its non-admin, relationally-authorized counterpart (design.md §8, the
// ACL-management import refactor) — both need the same exemption.
const (
	importUploadPath           = "/api/admin/import"
	relationalImportUploadPath = "/api/import"
)

// bypassGlobalBodyCap reports whether a request should skip the global
// MaxRequestBytes wrapper and rely solely on its own, purpose-built body-size
// enforcement. Only POST importUploadPath / relationalImportUploadPath
// qualify: MaxRequestBytes is DoS hardening for the general surface, including
// unauthenticated routes (health probes, UI shell, bootstrap); these routes,
// by contrast, are authenticated (admin, or a relationally-authorized manager)
// and already enforce their own larger ImportMaxUploadBytes ceiling via
// http.MaxBytesReader inside enqueueImportShared. Wrapping them in the smaller
// global cap too would nest MaxBytesReaders, silently shrinking the effective
// limit to min(MaxRequestBytes, ImportMaxUploadBytes) instead of the intended,
// larger upload ceiling — so both are exempted here and governed solely by
// their own cap.
func bypassGlobalBodyCap(method, path string) bool {
	return method == http.MethodPost && (path == importUploadPath || path == relationalImportUploadPath)
}

// bodyCapConfig supplies the live max-request-bytes cap, read per request.
type bodyCapConfig interface {
	MaxRequestBytes() int64
}

// withGlobalBodyCap caps every request body at the live MaxRequestBytes, reading
// cfg per request; a value <= 0 (or a nil cfg) disables it, and requests matching
// bypassGlobalBodyCap reach next unwrapped (governed by their own cap).
func withGlobalBodyCap(next http.Handler, cfg bodyCapConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int64
		if cfg != nil {
			n = cfg.MaxRequestBytes()
		}
		if n <= 0 || bypassGlobalBodyCap(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
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

// readinessConfig reports whether a dead config listener should fail readiness.
type readinessConfig interface{ RequireConfigListener() bool }

// readyHandler exposes db ping status. Driver errors are logged but kept out of
// the response body — they can leak connection-string fragments or credentials.
// When RequireConfigListener is on, a dead listener also fails readiness (D7/D8).
func readyHandler(db *gorm.DB, cfg readinessConfig, listener listenerHealth) http.HandlerFunc {
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
		if cfg != nil && listener != nil && cfg.RequireConfigListener() && !listener.Healthy() {
			slog.Warn("readiness: config invalidation listener is down and require_config_listener is set")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("config listener down"))
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
