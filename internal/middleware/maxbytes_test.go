package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An oversized body must be rejected during the read — the reader returns
// *http.MaxBytesError and never buffers the full payload.
func TestMaxBytes_RejectsOversized(t *testing.T) {
	var readErr error
	var readN int
	h := MaxBytes(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readErr, readN = err, len(b)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/documents", strings.NewReader(strings.Repeat("x", 1000)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatalf("expected read to fail on oversized body, read %d bytes", readN)
	}
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Fatalf("want *http.MaxBytesError, got %v", readErr)
	}
	if readN > 10 {
		t.Fatalf("read %d bytes past the 10-byte cap", readN)
	}
}

func TestMaxBytes_AllowsUnderLimit(t *testing.T) {
	const payload = "hello"
	var got string
	h := MaxBytes(1 << 20)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("unexpected read error: %v", err)
		}
		got = string(b)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/documents", strings.NewReader(payload))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != payload {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}

// A non-positive ceiling disables the cap entirely.
func TestMaxBytes_ZeroDisablesCap(t *testing.T) {
	big := strings.Repeat("x", 5000)
	var readErr error
	h := MaxBytes(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/documents", strings.NewReader(big))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if readErr != nil {
		t.Fatalf("cap disabled but read failed: %v", readErr)
	}
}
