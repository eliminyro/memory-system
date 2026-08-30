package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// rlConfig is a fixed RateLimitConfig for the throttle tests.
type rlConfig struct {
	rps   float64
	burst int
	depth int
}

func (c rlConfig) RateLimitRPS() float64  { return c.rps }
func (c rlConfig) RateLimitBurst() int    { return c.burst }
func (c rlConfig) TrustedProxyDepth() int { return c.depth }

// With burst=2 the third request from the same client is throttled with 429.
func TestRateLimit_OverLimitReturns429(t *testing.T) {
	h := RateLimit(rlConfig{1, 2, 0})(okHandler())
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
	h := RateLimit(rlConfig{1, 1, 0})(okHandler())
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

// The cheap liveness probe is never throttled.
func TestRateLimit_ExemptsHealthProbe(t *testing.T) {
	h := RateLimit(rlConfig{1, 1, 0})(okHandler())
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/~/health", nil)
		req.RemoteAddr = "203.0.113.50:9" // same key every time
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/~/health call %d = %d, want 200 (exempt)", i, rec.Code)
		}
	}
}

// /~/ready pings the DB, so it is NOT exempt: past the burst it must throttle.
func TestRateLimit_ReadyIsThrottled(t *testing.T) {
	h := RateLimit(rlConfig{1, 1, 0})(okHandler())
	call := func() int {
		req := httptest.NewRequest(http.MethodGet, "/~/ready", nil)
		req.RemoteAddr = "203.0.113.51:9"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(); c != http.StatusOK {
		t.Fatalf("/~/ready first = %d, want 200", c)
	}
	if c := call(); c != http.StatusTooManyRequests {
		t.Fatalf("/~/ready second = %d, want 429 (not exempt)", c)
	}
}

// Depth 0 (default) ignores X-Forwarded-For entirely and keys on RemoteAddr's
// host — a spoofed XFF can't move the key.
func TestClientIP_Depth0IgnoresXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "9.9.9.9:4321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(req, 0); got != "9.9.9.9" {
		t.Fatalf("clientIP(depth 0) = %q, want 9.9.9.9 (XFF ignored)", got)
	}
}

// Depth 1 keys on the last XFF entry — the IP the single trusted proxy observed.
func TestClientIP_Depth1UsesLastHop(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "5.6.7.8, 203.0.113.9")
	if got := clientIP(req, 1); got != "203.0.113.9" {
		t.Fatalf("clientIP(depth 1) = %q, want 203.0.113.9", got)
	}
}

// A client-spoofed extra leftmost XFF entry does NOT change the depth-1 key:
// only the rightmost (trusted) hop is read.
func TestClientIP_Depth1IgnoresSpoofedLeftmost(t *testing.T) {
	base := httptest.NewRequest(http.MethodGet, "/", nil)
	base.RemoteAddr = "10.0.0.1:1234"
	base.Header.Set("X-Forwarded-For", "203.0.113.9")
	want := clientIP(base, 1)

	spoofed := httptest.NewRequest(http.MethodGet, "/", nil)
	spoofed.RemoteAddr = "10.0.0.1:1234"
	spoofed.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(spoofed, 1); got != want {
		t.Fatalf("spoofed leftmost changed key: got %q, want %q", got, want)
	}
}

// A too-short XFF (fewer entries than the trusted depth) falls back to RemoteAddr
// rather than trusting a client-supplied value. Covers both the missing-header
// case at depth 1 and the fewer-than-N-entries case at depth 2.
func TestClientIP_ShortXFFFallsBack(t *testing.T) {
	missing := httptest.NewRequest(http.MethodGet, "/", nil)
	missing.RemoteAddr = "9.9.9.9:4321" // no XFF header at all
	if got := clientIP(missing, 1); got != "9.9.9.9" {
		t.Fatalf("clientIP(depth 1, no XFF) = %q, want 9.9.9.9 (fallback)", got)
	}

	shortHdr := httptest.NewRequest(http.MethodGet, "/", nil)
	shortHdr.RemoteAddr = "9.9.9.9:4321"
	shortHdr.Header.Set("X-Forwarded-For", "5.6.7.8") // 1 entry, depth 2 needs 2
	if got := clientIP(shortHdr, 2); got != "9.9.9.9" {
		t.Fatalf("clientIP(depth 2, short XFF) = %q, want 9.9.9.9 (fallback)", got)
	}
}
