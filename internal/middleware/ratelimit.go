package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxVisitors bounds the per-key limiter map so a flood of distinct keys
// (spoofed X-Forwarded-For) can't exhaust memory; idle entries evicted past this.
const maxVisitors = 8192

// visitorTTL is how long an idle key's limiter is retained before eviction.
const visitorTTL = 10 * time.Minute

// rateLimitExempt reports paths never throttled: only the cheap, DB-free liveness
// probe. /~/ready pings the DB, so it stays throttled to protect the small pool.
func rateLimitExempt(p string) bool {
	return p == "/~/health"
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

// rateKey derives the throttle key from the trusted client IP. RateLimit runs
// OUTSIDE the mux, before any auth middleware populates the subject, so there is
// no principal to key on here. Per-principal limiting would require moving
// RateLimit inside the authenticated sub-stacks — a deliberate follow-up, out of
// scope.
func rateKey(r *http.Request, trustedProxyDepth int) string {
	return "ip:" + clientIP(r, trustedProxyDepth)
}

// clientIP derives the caller IP in a spoof-safe, proxy-depth-aware way.
// trustedProxyDepth 0 (default) ignores X-Forwarded-For entirely and keys on the
// RemoteAddr host — unspoofable. Depth N>=1 takes the X-Forwarded-For entry N
// positions from the right (index len-N), i.e. the IP the outermost TRUSTED proxy
// observed. If the header is missing or has fewer than N entries it falls back to
// RemoteAddr — a short or absent header is never trusted.
func clientIP(r *http.Request, trustedProxyDepth int) string {
	if trustedProxyDepth >= 1 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if idx := len(parts) - trustedProxyDepth; idx >= 0 {
				return strings.TrimSpace(parts[idx])
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimitConfig supplies the live rate-limit knobs, read per request so a
// config change applies without a restart. RPS <= 0 disables the throttle.
type RateLimitConfig interface {
	RateLimitRPS() float64
	RateLimitBurst() int
	TrustedProxyDepth() int
}

// dynamicLimiter swaps in a fresh token-bucket limiter only when rps or burst
// change; per-key limiterFor calls run on the current *rateLimiter. mu makes the
// rebuild concurrency-safe across the many request goroutines.
type dynamicLimiter struct {
	mu    sync.Mutex
	rps   float64
	burst int
	rl    *rateLimiter
}

func (d *dynamicLimiter) current(rps float64, burst int) *rateLimiter {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rl == nil || d.rps != rps || d.burst != burst {
		d.rps, d.burst, d.rl = rps, burst, newRateLimiter(rps, burst)
	}
	return d.rl
}

// RateLimit is a token-bucket throttle keyed by the trusted client IP, reading
// rps/burst/proxy-depth live from cfg per request; rps <= 0 or a nil cfg
// disables it, and the limiter is rebuilt only when rps or burst change.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	dyn := &dynamicLimiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg == nil || cfg.RateLimitRPS() <= 0 || rateLimitExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			rl := dyn.current(cfg.RateLimitRPS(), cfg.RateLimitBurst())
			if !rl.limiterFor(rateKey(r, cfg.TrustedProxyDepth())).Allow() {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
