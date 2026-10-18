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
	"math"
	"net"
	"net/http"
	"strconv"
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
	// key overrides the bucket key (default clientIP). Used by the demo
	// limiter to key on CF-Connecting-IP and to add a global bucket.
	key func(*http.Request) string
	// onDeny, when set, is called once for every request the limiter
	// rejects (the demo counts 429s by class; no addresses).
	onDeny func()

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
// Requires the clientip middleware upstream so r.RemoteAddr reflects the
// real client IP (Cloudflare's CF-Connecting-IP, from a trusted proxy).
func (l *ipLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if l.key != nil {
			ip = l.key(r)
		}
		if ok, wait := l.allow(ip); !ok {
			if l.onDeny != nil {
				l.onDeny()
			}
			// Honest: the time until the bucket has a token again, not a guess.
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
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
func (l *ipLimiter) allow(ip string) (bool, time.Duration) {
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
	// Reserve instead of Allow so a refusal can say how long until the next
	// token; a refused reservation is cancelled and costs nothing.
	res := b.l.Reserve()
	if d := res.Delay(); d > 0 || !res.OK() {
		res.Cancel()
		if !res.OK() || d <= 0 {
			d = time.Duration(float64(time.Second) / float64(l.rps))
		}
		return false, d
	}
	return true, 0
}

// clientIP extracts the calling IP from r.RemoteAddr, which the
// clientip middleware has already set to the real visitor (CF-Connecting-IP,
// believed only from a trusted proxy), so the host portion is the key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Signup-request limits: per real visitor, and a global backstop per pod.
const (
	signupReqPerIPPerHour  = 5
	signupReqGlobalPerHour = 200
)

// newSignupRequestLimiters builds the per-visitor limiter and the global
// backstop for POST /api/signup/request. Chain them in that order, so a
// request the visitor's own limit refuses never spends a global token.
func newSignupRequestLimiters(perIP, global int) (*ipLimiter, *ipLimiter) {
	one := newIPLimiter(perIP, time.Hour, time.Hour)
	all := newIPLimiter(global, time.Hour, time.Hour)
	all.key = func(*http.Request) string { return "global-signup-request" }
	return one, all
}
