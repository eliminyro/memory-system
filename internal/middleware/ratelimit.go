package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/eliminyro/memory-system/internal/auth"
)

// maxVisitors bounds the per-key limiter map so a flood of distinct keys
// (spoofed X-Forwarded-For) can't exhaust memory; idle entries evicted past this.
const maxVisitors = 8192

// visitorTTL is how long an idle key's limiter is retained before eviction.
const visitorTTL = 10 * time.Minute

// rateLimitExempt reports paths never throttled: liveness/readiness probes
// (throttling them could pull a healthy instance out of rotation).
func rateLimitExempt(p string) bool {
	return p == "/~/health" || p == "/~/ready"
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type rateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rps      rate.Limit
	burst    int
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	return &rateLimiter{
		visitors: make(map[string]*visitor),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// limiterFor returns the token bucket for a key, creating it on first use.
// Idle-key eviction runs opportunistically once the map exceeds maxVisitors.
func (rl *rateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if v, ok := rl.visitors[key]; ok {
		v.lastSeen = time.Now()
		return v.limiter
	}
	if len(rl.visitors) >= maxVisitors {
		rl.evictLocked()
	}
	lim := rate.NewLimiter(rl.rps, rl.burst)
	rl.visitors[key] = &visitor{limiter: lim, lastSeen: time.Now()}
	return lim
}

// evictLocked drops idle entries; caller holds rl.mu. If nothing is idle the
// map is cleared wholesale — bounded fallback keeping memory finite under a flood.
func (rl *rateLimiter) evictLocked() {
	cutoff := time.Now().Add(-visitorTTL)
	evicted := false
	for k, v := range rl.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(rl.visitors, k)
			evicted = true
		}
	}
	if !evicted {
		rl.visitors = make(map[string]*visitor)
	}
}

// rateKey derives the throttle key: authenticated principal when present, else
// client IP. Unauth surfaces (/oauth/token, DCR) have no principal, so IP is the
// intended abuse control there.
func rateKey(r *http.Request) string {
	if subj, ok := auth.SubjectFromContext(r.Context()); ok && subj.ID != "" {
		return "sub:" + subj.ID
	}
	return "ip:" + clientIP(r)
}

// clientIP extracts the caller IP, honouring the first X-Forwarded-For hop
// (server runs behind an ingress) and falling back to the RemoteAddr host.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimit is a token-bucket throttle keyed by principal (falling back to
// client IP). rps is the sustained refill rate, burst the bucket size; over-limit
// requests get 429 + Retry-After. Probes are exempt. Outer abuse control over the
// auth/write surfaces; reads share the same bucket as defence in depth.
func RateLimit(rps float64, burst int) func(http.Handler) http.Handler {
	rl := newRateLimiter(rps, burst)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rateLimitExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if !rl.limiterFor(rateKey(r)).Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
