package middleware

import (
	"net/http"
	"strings"
)

// CORS allows cross-origin requests from browser-based OAuth public clients
// (MCP Inspector on localhost, Claude.ai's custom-connector backend, etc.)
// to the public OAuth surface: /oauth/*, /.well-known/*, and /mcp.
//
// memory-mcp has no same-product browser UI on its own origin, so unlike
// hilo's CORS this does not maintain an allowlist of credentialed origins.
// All OAuth/MCP requests get the request Origin reflected; credentials
// (cookies) are never allowed — clients on these endpoints use Bearer JWT
// auth and PKCE binds the code to the registered client_id.
//
// Anything else gets no CORS headers and the browser blocks the cross-origin
// response by default.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && isPublicOAuthPath(r.URL.Path) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
			} else {
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			}
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Add("Vary", "Origin")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isPublicOAuthPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/oauth/"):
		return true
	case strings.HasPrefix(p, "/.well-known/"):
		return true
	case p == "/mcp" || strings.HasPrefix(p, "/mcp/"):
		return true
	}
	return false
}
