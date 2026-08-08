package middleware

import (
	"net/http"
	"strings"
)

// CORS reflects the request Origin on the public OAuth/MCP surface (/oauth/*,
// /.well-known/*, /mcp) for browser-based public clients. Credentials are never
// allowed — Bearer JWT + PKCE secures these endpoints, so no credentialed-origin
// allowlist is kept (unlike hilo). Other paths get no CORS headers.
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
