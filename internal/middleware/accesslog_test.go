package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AccessLog seeds a request id into context, exposes it via RequestIDFromContext,
// and echoes it in the X-Request-Id response header.
func TestAccessLog_SetsRequestID(t *testing.T) {
	var seen string
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if seen == "" {
		t.Fatal("handler saw no request id in context")
	}
	if got := rec.Header().Get("X-Request-Id"); got != seen {
		t.Fatalf("X-Request-Id = %q, want %q", got, seen)
	}
}

func TestRequestIDFromContext_Absent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	if id := RequestIDFromContext(req.Context()); id != "" {
		t.Fatalf("id = %q, want empty when unset", id)
	}
}

// statusRecorder records the status and byte count while writing through to the
// underlying ResponseWriter (no buffering).
func TestStatusRecorder_CapturesStatusAndBytes(t *testing.T) {
	under := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: under}
	rec.WriteHeader(http.StatusTeapot)
	n, err := rec.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 5 || rec.bytes != 5 {
		t.Fatalf("bytes = %d/%d, want 5", n, rec.bytes)
	}
	if rec.status != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.status, http.StatusTeapot)
	}
	if under.Body.String() != "hello" {
		t.Fatalf("underlying body = %q, want hello", under.Body.String())
	}
}

// A bare Write (no explicit WriteHeader) defaults the recorded status to 200.
func TestStatusRecorder_DefaultsToOKOnWrite(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if _, err := rec.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.status)
	}
}
