package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/flags"
)

// Public read-only demo (B7b). When demo.Enabled (RIVOLT_DEMO=true on a
// replay install), a signed-out visitor's GET to a small allowlist of
// read endpoints is served as the demo user, and only for the demo
// vehicle. Nothing else changes: writes, settings, admin and every
// other path stay 401/404 for signed-out requests, a signed-in user is
// never switched, and with the env off the middleware is not installed.

const demoRatePerMin = 120

// demoHeader marks responses served to the anonymous demo visitor.
const demoHeader = "X-Rivolt-Demo"

// demoExactPaths are the allowlisted read endpoints (GET/HEAD only).
// They are exact matches: sub-paths such as /api/charges/clusters or
// /api/ledger/export are NOT included.
var demoExactPaths = map[string]bool{
	"/api/vehicles":               true,
	"/api/ledger/enabled":         true,
	"/api/ledger/monthly":         true,
	"/api/ledger/summary":         true,
	"/api/charges":                true,
	"/api/drives":                 true,
	"/api/ledger/trips":           true,
	"/api/sleep-activity":         true,
	"/api/charges/clusters":       true,
	"/api/settings/charging":      true, // GET only: home rate for the demo cost labels
	"/api/settings/home-location": true,
	// Curve data for the demo's drive/charge detail views. Results are
	// scoped to the demo user by the store; the limit is clamped below.
	"/api/samples": true,
}

// Anonymous /api/samples is the one heavy read (a full reply is 1-2 MB),
// so it gets its own, much tighter budget on top of the general demo
// limits: per client and install-wide, per minute. This bounds egress
// rather than the time window.
const (
	demoSamplesPerClientPerMin = 20
	demoSamplesGlobalPerMin    = 200
)

// demoSamplesDefaultLimit / demoSamplesMaxLimit bound an anonymous
// /api/samples request (the store treats limit<=0 as "no cap").
const (
	demoSamplesDefaultLimit = 2000
	demoSamplesMaxLimit     = 5000
)

// demoTilesPath is the one anonymous tile resource: the public basemap
// archive (read-only, byte-range GETs). chargers.pmtiles and anything
// else under /api/maps/tiles stay behind sign-in, and so does the
// Valhalla proxy (the demo draws stored polylines instead).
const demoTilesPath = "/api/maps/tiles/us.pmtiles"

// A map view issues dozens of range reads, so tiles get their own
// per-client and install-wide budgets and do NOT draw on the general
// demo limits (a map must not starve the data calls, or the reverse).
const (
	demoTilesPerClientPerMin = 1200
	demoTilesGlobalPerMin    = 12000
)

// demoPerDriveSuffixes are the per-drive GET sub-resources the demo's
// drive detail view reads. {id} is a single, plain path segment; the
// handlers scope by the demo user's id, so another user's drive id is
// a 404 (weather) or an empty series, never data. Deliberately NOT
// included: /efficiency (the AI recap) and anything that writes.
var demoPerDriveSuffixes = []string{"/weather", "/weather/series"}

// demoPerDrivePath reports whether p is /api/drives/{id}<suffix> for an
// allowed suffix and a plain id segment.
func demoPerDrivePath(p string) bool {
	const prefix = "/api/drives/"
	if !strings.HasPrefix(p, prefix) {
		return false
	}
	rest := p[len(prefix):]
	for _, suf := range demoPerDriveSuffixes {
		if !strings.HasSuffix(rest, suf) {
			continue
		}
		id := strings.TrimSuffix(rest, suf)
		if id == "" || len(id) > 128 {
			return false
		}
		for _, r := range id {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.', r == ':':
			default:
				return false
			}
		}
		return id != "." && id != ".."
	}
	return false
}

// clampDemoSamplesLimit bounds the limit query param of an anonymous
// /api/samples request in place.
func clampDemoSamplesLimit(r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("limit"))
	switch {
	case n <= 0:
		n = demoSamplesDefaultLimit
	case n > demoSamplesMaxLimit:
		n = demoSamplesMaxLimit
	}
	q.Set("limit", strconv.Itoa(n))
	r.URL.RawQuery = q.Encode()
}

// demoAllowed reports whether method+path may be served to an
// anonymous visitor as the demo user. vehicleID is the demo vehicle's
// Rivian id: per-vehicle routes are allowed for that id only.
func demoAllowed(method, path, vehicleID string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	p := strings.TrimSuffix(path, "/")
	if demoExactPaths[p] || path == demoTilesPath {
		return true
	}
	return demoPerDrivePath(p) ||
		p == "/api/state/"+vehicleID ||
		p == "/api/vehicles/"+vehicleID+"/now" ||
		p == "/api/vehicles/"+vehicleID+"/pack-health"
}

// demoAccessMW serves allowlisted anonymous requests as the demo user.
// It runs after authentication resolves the real identity and before
// impersonation, so a real session always wins and impersonation sees
// only the (non-admin) demo user.
func demoAccessMW(uid uuid.UUID, vehicleID string, limiter, global *ipLimiter) func(http.Handler) http.Handler {
	samples := newIPLimiter(demoSamplesPerClientPerMin, time.Minute, time.Hour)
	samples.key = demoClientKey
	samplesGlobal := newIPLimiter(demoSamplesGlobalPerMin, time.Minute, time.Hour)
	samplesGlobal.key = func(*http.Request) string { return "global-samples" }
	tiles := newIPLimiter(demoTilesPerClientPerMin, time.Minute, time.Hour)
	tiles.key = demoClientKey
	tilesGlobal := newIPLimiter(demoTilesGlobalPerMin, time.Minute, time.Hour)
	tilesGlobal.key = func(*http.Request) string { return "global-tiles" }
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, signedIn := auth.UserFromContext(r.Context()); signedIn || !demoAllowed(r.Method, r.URL.Path, vehicleID) {
				next.ServeHTTP(w, r)
				return
			}
			serve := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(demoHeader, "1")
				if strings.TrimSuffix(r.URL.Path, "/") == "/api/samples" {
					clampDemoSamplesLimit(r)
				}
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uid)))
			}))
			if r.URL.Path == demoTilesPath {
				// Per-client before global (see below), own budgets.
				tiles.Middleware(tilesGlobal.Middleware(serve)).ServeHTTP(w, r)
				return
			}
			// Order matters: a token bucket is charged when it is
			// checked, so every per-client limiter runs BEFORE any
			// install-wide one. Otherwise one noisy client (rejected by
			// its own limit) would still drain the global budget and
			// starve everyone else.
			isSamples := strings.TrimSuffix(r.URL.Path, "/") == "/api/samples"
			if isSamples {
				serve = samplesGlobal.Middleware(serve)
			}
			serve = global.Middleware(serve)
			if isSamples {
				serve = samples.Middleware(serve)
			}
			limiter.Middleware(serve).ServeHTTP(w, r)
		})
	}
}

// handleDemoInfo serves GET /api/demo: where the SPA finds the demo
// vehicle. Registered only when the demo is enabled (404 otherwise).
func handleDemoInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"vehicle_id": demo.VehicleID(),
		"home_label": demo.HomeLabel,
	})
}

// moneyTabEnabledOrDemo is moneyTabEnabled plus the demo user while
// the demo is on, so the anonymous demo sees the Money tab without a
// per-user flag write.
func moneyTabEnabledOrDemo(store *flags.Store) func(string) bool {
	base := moneyTabEnabled(store)
	return func(userID string) bool {
		if demo.Enabled() && userID == demo.UserID().String() {
			return true
		}
		return base(userID)
	}
}

// demoRateGlobalPerMin caps the whole anonymous demo regardless of
// client address, so a spoofed or rotating address can't evade the
// per-client limit.
const demoRateGlobalPerMin = 1200

// demoClientKey keys the per-client bucket. Cloudflare sets
// CF-Connecting-IP (overwriting any client-supplied value) in front of
// every deployment; chi's RealIP trusts client-supplied X-Real-IP /
// X-Forwarded-For, so those are not used. Without the header (direct
// access, tests) it falls back to the connection address.
func demoClientKey(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// newDemoLimiter is the per-client limiter; newDemoGlobalLimiter the
// install-wide one. demoAccessMW applies both.
func newDemoLimiter() *ipLimiter {
	l := newIPLimiter(demoRatePerMin, time.Minute, time.Hour)
	l.key = demoClientKey
	return l
}

func newDemoGlobalLimiter() *ipLimiter {
	l := newIPLimiter(demoRateGlobalPerMin, time.Minute, time.Hour)
	l.key = func(*http.Request) string { return "global" }
	return l
}
