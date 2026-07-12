package middleware

import "net/http"

// MaxBytes wraps r.Body in an http.MaxBytesReader so an oversized body fails at
// the ceiling instead of being fully buffered (or embedded on the /api path).
// Covers the /api JSON decode and /mcp streamable paths. n <= 0 disables the cap.
func MaxBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}
