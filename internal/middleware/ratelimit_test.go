package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eliminyro/memory-system/internal/auth"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// With burst=2 the third request from the same client within the window is
// throttled with 429.
func TestRateLimit_OverLimitReturns429(t *testing.T) {
	h := RateLimit(1, 2)(okHandler())
	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/documents", nil)
		req.RemoteAddr = "203.0.113.7:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("req1 = %d, want 200", c)
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("req2 = %d, want 200", c)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/documents", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("req3 = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("429 response missing Retry-After header")
	}
}

// Distinct clients (different IPs) get independent buckets.
func TestRateLimit_KeyedPerClient(t *testing.T) {
	h := RateLimit(1, 1)(okHandler())
	hit := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/documents", nil)
		req.RemoteAddr = ip + ":1000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := hit("198.51.100.1"); c != http.StatusOK {
		t.Fatalf("client A first = %d, want 200", c)
	}
	// Second client's first request must not be blocked by client A.
	if c := hit("198.51.100.2"); c != http.StatusOK {
		t.Fatalf("client B first = %d, want 200", c)
	}
	// Client A's second request exhausts its burst-1 bucket -> 429.
	if c := hit("198.51.100.1"); c != http.StatusTooManyRequests {
		t.Fatalf("client A second = %d, want 429", c)
	}
}

// The principal (subject) key takes precedence over IP: two requests from the
// same IP but different subjects get independent buckets.
func TestRateLimit_KeyedByPrincipal(t *testing.T) {
	h := RateLimit(1, 1)(okHandler())
	hit := func(subjID string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/documents", nil)
		req.RemoteAddr = "192.0.2.9:2000" // same IP for both
		ctx := auth.WithSubject(context.Background(), auth.Subject{Type: auth.SubjectTypeUser, ID: subjID})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	if c := hit("user-a"); c != http.StatusOK {
		t.Fatalf("user-a first = %d, want 200", c)
	}
	if c := hit("user-b"); c != http.StatusOK {
		t.Fatalf("user-b first (same IP, diff subject) = %d, want 200", c)
	}
	if c := hit("user-a"); c != http.StatusTooManyRequests {
		t.Fatalf("user-a second = %d, want 429", c)
	}
}

// Health and readiness probes are never throttled.
func TestRateLimit_ExemptsProbes(t *testing.T) {
	h := RateLimit(1, 1)(okHandler())
	for _, path := range []string{"/~/health", "/~/ready"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.50:9" // same key every time
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s call %d = %d, want 200 (exempt)", path, i, rec.Code)
			}
		}
	}
}

func TestClientIP_ForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "5.6.7.8, 10.0.0.1")
	if got := clientIP(req); got != "5.6.7.8" {
		t.Fatalf("clientIP = %q, want 5.6.7.8", got)
	}
}

func TestClientIP_RemoteAddrFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "9.9.9.9:4321"
	if got := clientIP(req); got != "9.9.9.9" {
		t.Fatalf("clientIP = %q, want 9.9.9.9", got)
	}
}
