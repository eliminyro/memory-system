package authletas

import (
	"net/http"
	"strings"
)

// DualAuth returns middleware that splits incoming requests by the shape
// of the Authorization header: requests bearing a Bearer token that looks
// like a JWT (three dot-separated segments) flow through the authlet
// bearer middleware, everything else falls back to memory-system's
// existing legacy auth.
//
// This is the transition-window adapter. Once Claude.ai (and any other
// downstream) has been on OAuth for a release, the /mcp mount drops
// DualAuth and uses BearerMW directly — at which point this method
// (and the legacy API-key middleware) can be deleted.
//
// legacy is the existing /mcp auth middleware (memory-system's API-key
// validator). It is invoked for any request whose Authorization header is
// not a 3-segment JWT — including no header at all, opaque tokens,
// cookies, and Basic auth.
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

// looksLikeJWT returns true when token is exactly two dots — i.e. a
// header.payload.signature triple. It is a routing heuristic, not a
// validator: the bearer middleware does the actual cryptographic check.
//
// We deliberately reject 3+ dots so JWS general-serialization payloads
// (rare in OAuth contexts) don't accidentally route to the AS verifier
// only to fail there with an unhelpful error.
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
