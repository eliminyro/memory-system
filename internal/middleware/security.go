package middleware

import "net/http"

// contentSecurityPolicy locks the UI down to same-origin assets only. The
// embedded page loads marked + DOMPurify + app.js from /ui/vendor and /ui,
// carries no inline scripts, and fetches only same-origin JSON, so 'self' is
// sufficient. object-src/base-uri/frame-ancestors 'none' close the usual
// clickjacking / <base> / plugin vectors even if the markup ever regresses.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// SecurityHeaders sets a strict Content-Security-Policy plus the companion
// nosniff / no-referrer headers on every response. It is the outermost layer
// so 429/413/OPTIONS responses carry the headers too. Defence in depth behind
// the DOMPurify sanitization in the UI: even a sanitizer bypass cannot load an
// off-origin script under this CSP.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
