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
	"github.com/eliminyro/memory-system/internal/version"
)

// Deps is the dependency bundle for NewHandler. Callers (cmd/server, e2e
// harness) build it once and hand it over; NewHandler owns the wiring.
type Deps struct {
	DB            *gorm.DB
	MCPServer     *mcp.Server
	KeyValidator  *auth.APIKeyValidator
	AuthletWiring *authletas.Wiring // optional; nil = API-key-only path
}

// NewHandler returns the full HTTP handler (mux + middleware) for the
// memory-system server. Production main wraps it in *http.Server;
// integration tests wrap it in httptest.NewServer.
func NewHandler(d Deps) http.Handler {
	mux := http.NewServeMux()

	apiKeyMW := auth.APIKeyMiddleware(d.KeyValidator)

	// MCP endpoints. When authlet wiring is present, requests with a
	// JWT-shaped Bearer token route through authlet's bearer middleware;
	// everything else falls back to the legacy API-key middleware. WWWAuth401
	// wraps both paths so OAuth-discovering clients always see a
	// WWW-Authenticate challenge on 401. UserContextBridge translates JWT
	// claims into auth.WithTenantID / auth.WithEmail so handlers see the
	// same context shape as on the API-key path.
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

	// Operational endpoints live under the /~/ prefix (liveness, readiness,
	// version). They are unauthenticated — probes and version checks must work
	// without credentials.
	mux.HandleFunc("/~/health", healthHandler)
	mux.HandleFunc("/~/ready", readyHandler(d.DB))
	mux.HandleFunc("/~/version", versionHandler)

	return middleware.CORS(mux)
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readyHandler exposes db ping status. Driver errors are logged but never
// written to the response body — driver error strings can contain
// connection-string fragments or credentials, so we keep them out of probe
// responses.
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
	})
}
