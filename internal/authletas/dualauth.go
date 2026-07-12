package authletas

import (
	"net/http"
	"strings"
)

// DualAuth splits requests by Authorization header shape: a JWT-looking Bearer
// token (three dot-separated segments) goes through the authlet bearer
// middleware, everything else (no header, opaque tokens, cookies, Basic) falls
// back to the legacy API-key middleware. Transition-window adapter: once
// downstreams are on OAuth, /mcp uses BearerMW directly and this (plus legacy)
// can be deleted.
func (w *Wiring) DualAuth(legacy func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	bearer := w.BearerMW
	return func(next http.Handler) http.Handler {
		bearerH := bearer(next)
		legacyH := legacy(next)
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if strings.HasPrefix(h, "Bearer ") &&
				looksLikeJWT(strings.TrimPrefix(h, "Bearer ")) {
				bearerH.ServeHTTP(rw, r)
				return
			}
			legacyH.ServeHTTP(rw, r)
		})
	}
}

// looksLikeJWT reports whether token has exactly two dots (header.payload.sig).
// Routing heuristic only — the bearer middleware does the crypto check. Rejects
// 3+ dots so JWS general-serialization payloads don't misroute to the verifier.
func looksLikeJWT(token string) bool {
	if token == "" {
		return false
	}
	count := 0
	for _, c := range token {
		if c == '.' {
			count++
			if count >= 3 {
				return false
			}
		}
	}
	return count == 2
}
