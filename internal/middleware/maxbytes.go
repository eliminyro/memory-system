package middleware

import "net/http"

// MaxBytes caps the size of every request body by wrapping r.Body in an
// http.MaxBytesReader before the downstream handler reads it. Reads past the
// ceiling fail with *http.MaxBytesError, so an oversized body is never fully
// buffered (and, on the API path, never embedded) — the read stops at the
// limit. A ceiling <= 0 disables the cap (the wrapper is skipped).
//
// This covers every body-reading path it wraps: the /api JSON Decode calls and
// the /mcp streamable handler.
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
