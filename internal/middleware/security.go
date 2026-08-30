package middleware

import "net/http"

// contentSecurityPolicy locks the UI to same-origin assets only: the embedded
// page has no inline scripts and fetches only same-origin JSON, so 'self' suffices.
// object-src/base-uri/frame-ancestors 'none' close clickjacking/<base>/plugin vectors.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// SecurityHeaders sets the CSP plus nosniff/no-referrer on every response.
// Outermost layer, so 429/413/OPTIONS responses carry them too; defence in depth
// behind the UI's DOMPurify — even a sanitizer bypass can't load an off-origin script.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
