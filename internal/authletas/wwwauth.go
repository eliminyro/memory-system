package authletas

import "net/http"

// WWWAuth401 injects an RFC 6750 / RFC 9728 WWW-Authenticate challenge on any
// downstream 401 unless the inner handler already set the header. Covers the
// legacy API-key path (BearerMW emits its own) so first-contact MCP clients can
// always discover the AS.
func (w *Wiring) WWWAuth401() func(http.Handler) http.Handler {
	challenge := `Bearer realm="MCP"`
	if w.prmURL != "" {
		challenge += `, resource_metadata="` + w.prmURL + `"`
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&challengeWriter{ResponseWriter: rw, challenge: challenge}, r)
		})
	}
}

type challengeWriter struct {
	http.ResponseWriter
	challenge string
}

func (cw *challengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized && cw.Header().Get("WWW-Authenticate") == "" {
		cw.Header().Set("WWW-Authenticate", cw.challenge)
	}
	cw.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying Flusher/Hijacker —
// required for MCP's SSE response path.
func (cw *challengeWriter) Unwrap() http.ResponseWriter { return cw.ResponseWriter }
