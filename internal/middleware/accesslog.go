package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/auth"
)

type requestIDKey struct{}

// RequestIDFromContext returns the per-request id set by AccessLog, or "" when
// none is present. The MCP tool-call logger reads it to correlate a tool
// dispatch with its HTTP access line.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID returns 8 random bytes hex-encoded, or "" if the RNG fails.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// statusRecorder captures the status code and byte count while writing straight
// through — it never buffers the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush and Unwrap preserve streaming (SSE) through the wrapper: the MCP
// Streamable-HTTP transport flushes, and http.ResponseController uses Unwrap.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// AccessLog logs one metadata line per request. Outermost so the status covers
// auth/rate-limit rejections; bodies, query strings and auth headers are never
// logged, and probe endpoints log at Debug.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := newRequestID()
		if rid != "" {
			r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, rid))
			w.Header().Set("X-Request-Id", rid)
		}
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		logAccess(r, rec, rid, time.Since(start))
	})
}

func logAccess(r *http.Request, rec *statusRecorder, rid string, d time.Duration) {
	attrs := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"duration_ms", d.Milliseconds(),
		"bytes", rec.bytes,
	}
	if rid != "" {
		attrs = append(attrs, "request_id", rid)
	}
	if tid := auth.TenantIDFromContext(r.Context()); tid != uuid.Nil {
		attrs = append(attrs, "tenant_id", tid.String())
	}
	if subj, ok := auth.SubjectFromContext(r.Context()); ok && subj.ID != "" {
		attrs = append(attrs, "subject", subj.ID)
	}

	const msg = "http request"
	switch {
	case isProbePath(r.URL.Path):
		slog.Default().Debug(msg, attrs...)
	case rec.status >= 500:
		slog.Default().Error(msg, attrs...)
	case rec.status == http.StatusTooManyRequests:
		slog.Default().Warn(msg, attrs...)
	default:
		slog.Default().Info(msg, attrs...)
	}
}

// isProbePath reports the liveness/readiness/version endpoints, which log at
// Debug to keep probe traffic out of the access log.
func isProbePath(p string) bool {
	switch p {
	case "/~/health", "/~/ready", "/~/version":
		return true
	}
	return false
}
