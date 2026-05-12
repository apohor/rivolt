package api

// Per-IP in-memory token bucket for hot public endpoints. Used for
// /api/signup/request as a last-line defense in case the Cloudflare
// edge rule ever fails open. State lives in-process so it resets on
// pod restart and is not shared across replicas — fine for the
// beta-scale signup form where ~5 req/IP/hour is plenty and edge
// shedding is the real defense.
//
// A LRU-style janitor reaps idle limiter entries every hour so a
// long-running pod under address rotation doesn't grow the map
// without bound.

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter rate-limits a route to per-IP buckets. Construct via
// newIPLimiter; call .Middleware on the desired chi sub-route.
type ipLimiter struct {
	rps     rate.Limit
	burst   int
	idleTTL time.Duration

	mu      sync.Mutex
	buckets map[string]*ipBucket
}

type ipBucket struct {
	l        *rate.Limiter
	lastSeen time.Time
}

// newIPLimiter returns a limiter that allows `burst` requests
// immediately and refills at `perWindow / window` rps thereafter.
// Idle buckets are reaped after idleTTL of inactivity.
func newIPLimiter(perWindow int, window, idleTTL time.Duration) *ipLimiter {
	return &ipLimiter{
		rps:     rate.Limit(float64(perWindow) / window.Seconds()),
		burst:   perWindow,
		idleTTL: idleTTL,
		buckets: make(map[string]*ipBucket),
	}
}

// Middleware returns the chi handler that enforces the bucket.
// Requires chi's middleware.RealIP upstream so r.RemoteAddr reflects
// the client IP (Cloudflare-set CF-Connecting-IP / X-Forwarded-For).
func (l *ipLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !l.allow(ip) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error": "too many requests, try again later",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow checks and decrements the bucket for ip. Lazy-creates a new
// bucket on first sight and updates lastSeen for the janitor.
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	// Reap on the way through — cheap O(n) over a small map and
	// avoids a second goroutine just for cleanup.
	if len(l.buckets) > 1024 {
		for k, b := range l.buckets {
			if now.Sub(b.lastSeen) > l.idleTTL {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{l: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[ip] = b
	}
	b.lastSeen = now
	return b.l.Allow()
}

// clientIP extracts the calling IP from r.RemoteAddr. chi's
// middleware.RealIP already rewrites RemoteAddr from
// CF-Connecting-IP / X-Forwarded-For, so we can trust the host
// portion here.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
