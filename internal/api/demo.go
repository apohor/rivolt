package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/maps"
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
	"/api/ledger/savings":         true, // computed from the demo account's own sessions and example schedule
	"/api/ledger/summary":         true,
	"/api/charges":                true,
	"/api/drives":                 true,
	"/api/drives/route-thumbs":    true, // the demo list's route thumbnails: the demo user's own drives, at most 60 ids, a few points each
	"/api/ledger/trips":           true,
	"/api/sleep-activity":         true,
	"/api/charges/clusters":       true,
	"/api/settings/charging":      true, // GET only: home rate for the demo cost labels
	"/api/settings/units":         true, // GET only: the sample account's units (a visitor's client falls back to its own region)
	"/api/settings/home-location": true,
	// Demo parity: the price book (read only) and the seeded saved trip
	// list. Both are scoped to the demo user by their stores.
	"/api/settings/charging/networks": true,
	"/api/trips/saved":                true,
	// Curve data for the demo's drive/charge detail views. Results are
	// scoped to the demo user by the store; the limit is clamped below.
	"/api/samples": true,
	// The iPhone's demo mode: the milestones list (the demo user's own), and the windowed aggregates its Home and Activity
	// tabs read. The aggregates are heavy (each reads the window's drives and charges), so they are in demoWindowedPaths below.
	"/api/milestones":          true,
	"/api/activity":            true,
	"/api/charge-split":        true,
	"/api/chart-points":        true,
	"/api/insights/efficiency": true,
}

// demoWindowedPaths are the allowlisted reads that scan a time window of the demo user's drives and charges. They take a
// window and a vehicle, which demoWindowQuery normalizes (RFC 3339 times snapped to 5 minutes, a window of at most
// demoWindowMaxSpan, only the demo vehicle) so the heavy budget and cache key stay bounded, and they draw on the heavy budget.
var demoWindowedPaths = map[string]bool{
	"/api/activity": true, "/api/charge-split": true, "/api/chart-points": true, "/api/insights/efficiency": true,
}

// demoWindowMaxSpan bounds the window an anonymous caller may ask for: the demo's data is under four months, and the app asks
// for a month at a time.
const demoWindowMaxSpan = 95 * 24 * time.Hour

// demoWindowSnap is what a window's edges are rounded down to, so repeat loads within the cache's lifetime share a key.
const demoWindowSnap = 5 * time.Minute

// demoWindowQuery rewrites the query of a windowed anonymous read, in place, to the few parameters it may carry: since and
// until (RFC 3339, snapped, at most demoWindowMaxSpan apart), the demo vehicle (vehicle_id, or vehicle for the efficiency
// insight) and, for the efficiency insight, units. Anything else is dropped, and a bad value falls back to the endpoint's own
// default rather than an error.
func demoWindowQuery(r *http.Request, vehicleID string) {
	q := r.URL.Query()
	out := url.Values{}
	snap := func(t time.Time) time.Time { return t.UTC().Truncate(demoWindowSnap) }
	var since, until time.Time
	haveSince, haveUntil := false, false
	if t, err := time.Parse(time.RFC3339, q.Get("until")); err == nil {
		until, haveUntil = snap(t), true
	}
	if t, err := time.Parse(time.RFC3339, q.Get("since")); err == nil {
		since, haveSince = snap(t), true
	}
	if haveSince && haveUntil && !since.Before(until) {
		haveSince, haveUntil = false, false // an empty or reversed window: the endpoint's default
	}
	if haveSince && haveUntil && until.Sub(since) > demoWindowMaxSpan {
		since = until.Add(-demoWindowMaxSpan)
	}
	if haveSince && !haveUntil && time.Since(since) > demoWindowMaxSpan {
		haveSince = false // an open-ended window from long ago
	}
	if haveSince {
		out.Set("since", since.Format(time.RFC3339))
	}
	if haveUntil {
		out.Set("until", until.Format(time.RFC3339))
	}
	for _, name := range []string{"vehicle_id", "vehicle"} {
		if q.Get(name) == vehicleID && vehicleID != "" {
			out.Set(name, vehicleID)
		}
	}
	if strings.TrimSuffix(r.URL.Path, "/") == "/api/insights/efficiency" {
		if u := q.Get("units"); u == "imperial" || u == "metric" {
			out.Set("units", u)
		}
	}
	r.URL.RawQuery = out.Encode()
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
	// demoSamplesMaxSpan bounds until-since for anonymous callers.
	demoSamplesMaxSpan      = 24 * time.Hour
	demoSamplesDefaultLimit = 2000
	demoSamplesMaxLimit     = 5000
)

// demoTilesPath is the anonymous basemap archive (read-only, byte-range
// GETs). With demoChargersTilesPath it is one of two anonymous tile
// resources; anything else under /api/maps/tiles stays behind sign-in,
// and so does the Valhalla proxy (the demo draws stored polylines).
const demoTilesPath = "/api/maps/tiles/us.pmtiles"

// demoChargersTilesPath is the public charger archive (NREL data, no
// user data), read-only byte ranges like the basemap, on the same tile
// budgets. The corridor query (/api/maps/chargers-along) stays closed.
const demoChargersTilesPath = "/api/maps/tiles/chargers.pmtiles"

// demoTileMaxRange caps one anonymous tile read: 512 KiB by default (a real read
// is a header, a directory or one tile, tens of KB), so a request cannot ask for
// the archive in a few big gulps. RIVOLT_DEMO_TILE_MAX_RANGE_KB raises it, to at
// most 2048, if a very large tile is ever refused. A request with no Range (the
// whole archive) or a bigger one is refused instead.
func demoTileMaxRange() int64 {
	kb := demoBudgetEnv("RIVOLT_DEMO_TILE_MAX_RANGE_KB", 512)
	if kb > 2048 {
		kb = 2048
	}
	return int64(kb) << 10
}

// Byte budgets on top of the request budgets: the request limiter counts reads,
// not how much they carry. Each read is charged its requested range, per client
// and install-wide, and a refused one is a 429 with a Retry-After. The defaults
// are megabytes per hour; RIVOLT_DEMO_TILES_MB_PER_CLIENT_PER_HOUR and
// RIVOLT_DEMO_TILES_MB_GLOBAL_PER_HOUR set a deployment's own.
const (
	demoTileMBPerClientPerHour = 200
	demoTileMBGlobalPerHour    = 3000
)

func demoTileByteBudgets() (perClient, global int) {
	return demoBudgetEnv("RIVOLT_DEMO_TILES_MB_PER_CLIENT_PER_HOUR", demoTileMBPerClientPerHour) << 20,
		demoBudgetEnv("RIVOLT_DEMO_TILES_MB_GLOBAL_PER_HOUR", demoTileMBGlobalPerHour) << 20
}

// demoRangeLen is the number of bytes a validated Range header asks for (0 for
// none, which is a HEAD).
func demoRangeLen(h string) int {
	spec, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok {
		return 0
	}
	lo, hi, ok := strings.Cut(spec, "-")
	if !ok {
		return 0
	}
	if lo == "" {
		n, _ := strconv.ParseInt(hi, 10, 64)
		return int(max(n, 0))
	}
	a, err1 := strconv.ParseInt(lo, 10, 64)
	b, err2 := strconv.ParseInt(hi, 10, 64)
	if err1 != nil || err2 != nil || b < a {
		return 0
	}
	return int(b - a + 1)
}

// demoTileRangeOK validates a Range header for an anonymous tile read:
// exactly one "bytes=a-b" (or suffix "bytes=-n") range of at most
// demoTileMaxRange bytes. It returns the HTTP status to refuse with, or
// 0 when the request may proceed.
func demoTileRangeOK(h string) int {
	spec, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok || spec == "" || strings.Contains(spec, ",") {
		return http.StatusBadRequest
	}
	lo, hi, ok := strings.Cut(spec, "-")
	if !ok {
		return http.StatusBadRequest
	}
	if lo == "" { // suffix: the last n bytes
		n, err := strconv.ParseInt(hi, 10, 64)
		if err != nil || n <= 0 {
			return http.StatusBadRequest
		}
		if n > demoTileMaxRange() {
			return http.StatusRequestedRangeNotSatisfiable
		}
		return 0
	}
	a, err1 := strconv.ParseInt(lo, 10, 64)
	b, err2 := strconv.ParseInt(hi, 10, 64) // open-ended "a-" has no b: refused
	if err1 != nil || err2 != nil || a < 0 || b < a {
		return http.StatusBadRequest
	}
	if b-a+1 > demoTileMaxRange() {
		return http.StatusRequestedRangeNotSatisfiable
	}
	return 0
}

func isDemoTilePath(p string) bool { return p == demoTilesPath || p == demoChargersTilesPath }

// demoDEMPrefix is the anonymous terrain route: the demo's map draws a hillshade like the app's, from the same in-cluster mirror. Static
// public elevation data, one whole tile per GET; it spends the tile budgets (a tile is charged its real byte count) and needs no Range.
const demoDEMPrefix = "/api/maps/dem"

// demoDEMTileReserve is what a terrain tile is charged to be admitted, before its size is known; once the tile is served the budgets are
// charged the rest of its real byte count (a z8 tile is 126 KB), so a tile smaller than the reserve costs the reserve.
const demoDEMTileReserve = 16 << 10

// demoCountingWriter counts the body bytes a handler writes.
type demoCountingWriter struct {
	http.ResponseWriter
	n int
}

func (c *demoCountingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += n
	return n, err
}

func (c *demoCountingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// demoGlyphsPrefix is the anonymous label route: the demo's map needs the same label glyphs, embedded in the binary and public data.
const demoGlyphsPrefix = "/api/maps/glyphs"

// isDemoMapAssetPath reports whether p is a static map asset the anonymous demo may read: one terrain tile, or one shipped label glyph range.
func isDemoMapAssetPath(p string) bool {
	if rest, ok := strings.CutPrefix(p, demoDEMPrefix); ok && maps.DEMPath(rest) {
		return true
	}
	rest, ok := strings.CutPrefix(p, demoGlyphsPrefix)
	return ok && maps.GlyphSize(rest) > 0
}

// demoMapAssetCost is the byte charge of one static map asset: a glyph range by its size, a terrain tile by the admission reserve (its real size is charged after it is served).
func demoMapAssetCost(p string) int {
	if rest, ok := strings.CutPrefix(p, demoGlyphsPrefix); ok {
		return maps.GlyphSize(rest)
	}
	return demoDEMTileReserve
}

// demoHeavyTZ is the set of time zones an anonymous visitor may ask the
// heavy reads for. Anything else is dropped, so the query string can't be
// used to dodge the response cache.
var demoHeavyTZ = map[string]bool{
	"America/Los_Angeles": true, "America/Denver": true, "America/Phoenix": true,
	"America/Chicago": true, "America/New_York": true, "America/Anchorage": true,
	"Pacific/Honolulu": true, "UTC": true,
}

// demoHeavyQuery rewrites the query of a heavy anonymous read to the
// only parameter it may carry (tz, from the fixed set), in place.
func demoHeavyQuery(r *http.Request) {
	tz := strings.TrimSpace(r.URL.Query().Get("tz"))
	if !demoHeavyTZ[tz] {
		r.URL.RawQuery = ""
		return
	}
	r.URL.RawQuery = url.Values{"tz": {tz}}.Encode()
}

// The parked-drain read window-scans up to 30 days of vehicle_state and
// plan-vs-actual joins drives and the ledger over a padded window, so
// both get a small shared budget and a short response cache on top of
// the general demo limits.
const (
	demoHeavyPerClientPerMin = 20 // cache misses only; a month of the app's tabs is several distinct reads
	demoHeavyGlobalPerMin    = 60
	demoHeavyCacheTTL        = 5 * time.Minute
)

// A map view issues dozens of range reads, so tiles get their own
// per-client and install-wide budgets and do NOT draw on the general
// demo limits (a map must not starve the data calls, or the reverse).
// These are the defaults; a deployment sets its own with
// RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN and RIVOLT_DEMO_TILES_GLOBAL_PER_MIN
// (the public demo.rivolt.dev runs tighter, sized to its origin).
const (
	demoTilesPerClientPerMin = 1200
	demoTilesGlobalPerMin    = 12000
	demoTilesMaxPerMin       = 100000 // a sanity bound on the env values
)

// demoTileBudgets returns the per-client and install-wide tile budgets
// per minute: the env values when set to a whole number in
// [1, demoTilesMaxPerMin], else the defaults (with a warning, so a typo
// can't silently lift or zero the limit).
func demoTileBudgets() (perClient, global int) {
	return demoBudgetEnv("RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN", demoTilesPerClientPerMin),
		demoBudgetEnv("RIVOLT_DEMO_TILES_GLOBAL_PER_MIN", demoTilesGlobalPerMin)
}

func demoBudgetEnv(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > demoTilesMaxPerMin {
		slog.Warn("demo: ignoring invalid tile budget; using the default", "env", name, "value", v, "default", def)
		return def
	}
	return n
}

// demoPerDriveSuffixes are the per-drive GET sub-resources the demo's
// drive detail view reads. {id} is a single, plain path segment; the
// handlers scope by the demo user's id, so another user's drive id is
// a 404 (weather) or an empty series, never data. Deliberately NOT
// included: /efficiency (the AI recap) and anything that writes.
//
// The bare "" suffix is GET /api/drives/{id}, one drive with its route polyline:
// the same row the demo's list already serves, scoped to the demo user. It must
// stay last, so "{id}/weather" is matched as a suffix before the bare id.
var demoPerDriveSuffixes = []string{"/weather", "/weather/series", ""}

// demoDriveIDRe is the shape of a real drive id ("live_<vehicle>_d_<unix>" or
// "electrafi_<hash>_d_<unix>"). The bare GET /api/drives/{id} is open to the demo
// only for ids of this shape, so a future single-segment endpoint under /drives
// (an export, a summary) does not join the allowlist by accident.
var demoDriveIDRe = regexp.MustCompile(`^(live|electrafi)_[A-Za-z0-9:._-]+_d_[0-9]+$`)

// demoChargeIDRe is the shape of a charge id the demo serves: a recorded session ("live_<vehicle>_c_<unix>", an
// "electrafi_" import) or a seeded one ("demo_seed_…"). The bare GET /api/charges/{id} is open only for ids of this shape, so
// the single-segment routes under /charges that are not a charge (clusters, to-confirm) never join the allowlist by accident.
var demoChargeIDRe = regexp.MustCompile(`^(live|electrafi|demo_seed)_[A-Za-z0-9:._-]{1,120}$`)

// demoPerChargePath reports whether p is /api/charges/{id} for a plain charge id.
func demoPerChargePath(p string) bool {
	id, ok := strings.CutPrefix(p, "/api/charges/")
	return ok && len(id) <= 140 && demoChargeIDRe.MatchString(id) && id != "." && id != ".."
}

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
		if suf == "" && !demoDriveIDRe.MatchString(id) {
			return false
		}
		return id != "." && id != ".."
	}
	return false
}

// demoHeavyPath reports whether p is one of the anonymous reads that
// join or window-scan a lot of rows: parked drain, plan-vs-actual and the
// savings estimate (a month of home sessions against the example schedule).
func demoHeavyPath(p, vehicleID string) bool {
	return demoWindowedPaths[p] || p == "/api/vehicles/"+vehicleID+"/drain" || p == "/api/vehicles/"+vehicleID+"/sleep-health" || p == "/api/ledger/savings" || demoSavedTripActualPath(p)
}

// demoSavedTripActualPath reports whether p is
// /api/trips/saved/{uuid}/actual. The id must parse as a uuid, so no
// other shape (or traversal) reaches the handler.
func demoSavedTripActualPath(p string) bool {
	const prefix, suffix = "/api/trips/saved/", "/actual"
	if !strings.HasPrefix(p, prefix) || !strings.HasSuffix(p, suffix) || len(p) <= len(prefix)+len(suffix) {
		return false
	}
	id := p[len(prefix) : len(p)-len(suffix)]
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
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
	// Bound the window too: a bounded window is bucketed server-side
	// (a scan of every row in it), so an anonymous caller may not ask
	// for days. Charge and drive pages need well under 24 h.
	if u, err := time.Parse(time.RFC3339, q.Get("until")); err == nil {
		lo := u.Add(-demoSamplesMaxSpan)
		if sn, err := time.Parse(time.RFC3339, q.Get("since")); err != nil || sn.Before(lo) {
			q.Set("since", lo.UTC().Format(time.RFC3339))
		}
	}
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
	if demoExactPaths[p] || isDemoTilePath(path) || isDemoMapAssetPath(path) {
		return true
	}
	return demoPerDrivePath(p) || demoPerChargePath(p) || demoSavedTripActualPath(p) ||
		p == "/api/vehicles/"+vehicleID+"/drain" ||
		p == "/api/vehicles/"+vehicleID+"/sleep-health" ||
		p == "/api/state/"+vehicleID ||
		p == "/api/vehicles/"+vehicleID+"/now" ||
		p == "/api/vehicles/"+vehicleID+"/pack-health" ||
		p == "/api/vehicles/"+vehicleID+"/trends" ||
		p == "/api/vehicles/"+vehicleID+"/cold-range"
}

// demoOnlyPublic reports whether method+path may be served at all by the
// standalone demo (demo.OnlyEnabled): the SPA's config and health reads,
// demo discovery, the static preset trips, and the demo allowlist.
func demoOnlyPublic(method, path, vehicleID string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	p := strings.TrimSuffix(path, "/")
	switch p {
	case "/api/config", "/api/health", "/api/demo", "/api/demo/trips",
		// The iPhone's server check (static: the server, the API version, whether sign-in and push are on).
		"/api/app/config":
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/api/demo/trips/"); ok && strings.HasSuffix(rest, "/plan") {
		id := strings.TrimSuffix(rest, "/plan")
		return id != "" && !strings.Contains(id, "/")
	}
	return demoAllowed(method, path, vehicleID)
}

// demoOnlyGateMW makes every /api route outside demoOnlyPublic a 404 on
// the standalone demo, before authentication runs: no sign-in, signup,
// account, admin, Hydra/Kratos/OIDC or write surface exists there, signed
// in or not. Non-/api paths (the SPA, health probes) are untouched.
func demoOnlyGateMW(vehicleID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) &&
				!demoOnlyPublic(r.Method, r.URL.Path, vehicleID) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// demoPlainDownstream asks what is below it for an uncompressed answer: the response cache keeps the body it is given, so that body
// must not depend on one client's Accept-Encoding.
func demoPlainDownstream(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Accept-Encoding")
		next.ServeHTTP(w, r)
	})
}

// demoNegotiateEncoding compresses the JSON of a heavy anonymous read for a client that accepts gzip (or deflate), and says the answer
// varies by it.
func demoNegotiateEncoding(next http.Handler) http.Handler {
	compress := middleware.Compress(5, "application/json")(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		compress.ServeHTTP(w, r)
	})
}

// demoAccessMW serves allowlisted anonymous requests as the demo user.
// It runs after authentication resolves the real identity and before
// impersonation, so a real session always wins and impersonation sees
// only the (non-admin) demo user.
func demoAccessMW(ls *limiterSet, uid uuid.UUID, vehicleID string, limiter, global *ipLimiter) func(http.Handler) http.Handler {
	samples := ls.ip("demo-samples", demoSamplesPerClientPerMin, time.Minute, time.Hour)
	samples.key = demoClientKey
	samples.onDeny = demoDenied("samples", "client")
	samplesGlobal := ls.ip("demo-samples-global", demoSamplesGlobalPerMin, time.Minute, time.Hour)
	samplesGlobal.key = func(*http.Request) string { return "global-samples" }
	samplesGlobal.onDeny = demoDenied("samples", "global")
	tilesPerClient, tilesPerInstall := demoTileBudgets()
	tiles := ls.ip("demo-tiles", tilesPerClient, time.Minute, time.Hour)
	tiles.key = demoClientKey
	tiles.onDeny = demoDenied("tiles", "client")
	tilesGlobal := ls.ip("demo-tiles-global", tilesPerInstall, time.Minute, time.Hour)
	tilesGlobal.key = func(*http.Request) string { return "global-tiles" }
	tilesGlobal.onDeny = demoDenied("tiles", "global")
	bytesPerClient, bytesPerInstall := demoTileByteBudgets()
	tileBytes := ls.ip("demo-tile-bytes", bytesPerClient, time.Hour, time.Hour)
	tileBytes.key = demoClientKey
	tileBytes.onDeny = demoDenied("tile_bytes", "client")
	tileBytesGlobal := ls.ip("demo-tile-bytes-global", bytesPerInstall, time.Hour, time.Hour)
	tileBytesGlobal.key = func(*http.Request) string { return "global-tile-bytes" }
	tileBytesGlobal.onDeny = demoDenied("tile_bytes", "global")
	tileCost := func(r *http.Request) int { return demoRangeLen(r.Header.Get("Range")) }
	// The heavy reads (parked drain, plan-vs-actual) share one small
	// budget and one response cache.
	heavy := ls.ip("demo-heavy", demoHeavyPerClientPerMin, time.Minute, time.Hour)
	heavy.key = demoClientKey
	heavy.onDeny = demoDenied("heavy", "client")
	heavyGlobal := ls.ip("demo-heavy-global", demoHeavyGlobalPerMin, time.Minute, time.Hour)
	heavyGlobal.key = func(*http.Request) string { return "global-heavy" }
	heavyGlobal.onDeny = demoDenied("heavy", "global")
	heavyCache := newDemoResponseCache(demoHeavyCacheTTL, 32)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, signedIn := auth.UserFromContext(r.Context()); signedIn || !demoAllowed(r.Method, r.URL.Path, vehicleID) {
				next.ServeHTTP(w, r)
				return
			}
			p := strings.TrimSuffix(r.URL.Path, "/")
			tile := isDemoTilePath(r.URL.Path)
			asset := isDemoMapAssetPath(r.URL.Path)
			if !tile && !asset {
				// Demo-user data is served on the same URLs signed-in
				// users call: no shared or browser cache may keep one
				// side's response for the other.
				h := w.Header()
				h.Set("Cache-Control", "private, no-store")
				h.Add("Vary", "Cookie")
				h.Add("Vary", "Authorization")
			}
			serve := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(demoHeader, "1")
				if p == "/api/samples" {
					clampDemoSamplesLimit(r)
				}
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uid)))
			}))
			if asset {
				// A whole terrain tile: no Range. Admitted at a small reserve, same per-client then install-wide order as tiles; after the answer,
				// both byte budgets are charged the rest of the bytes it carried.
				cost := func(r *http.Request) int { return demoMapAssetCost(r.URL.Path) }
				counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					cw := &demoCountingWriter{ResponseWriter: w}
					serve.ServeHTTP(cw, r)
					if extra := cw.n - cost(r); extra > 0 && !strings.HasPrefix(r.URL.Path, demoGlyphsPrefix) {
						tileBytes.debit(tileBytes.key(r), extra)
						tileBytesGlobal.debit(tileBytesGlobal.key(r), extra)
					}
				})
				tiles.Middleware(tileBytes.MiddlewareN(cost)(
					tilesGlobal.Middleware(tileBytesGlobal.MiddlewareN(cost)(counted)))).ServeHTTP(w, r)
				return
			}
			if tile {
				// A byte-range read of bounded size only; per-client
				// before global (see below), own budgets.
				// (HEAD has no body, so it needs no range.)
				if code := demoTileRangeOK(r.Header.Get("Range")); code != 0 && r.Method != http.MethodHead {
					w.Header().Set("Content-Range", "bytes */*")
					http.Error(w, fmt.Sprintf("range required (a single bytes range of at most %d KiB)", demoTileMaxRange()>>10), code)
					return
				}
				// A conditional header can turn a range read into the whole file (a
				// mismatched If-Range makes a server ignore the Range), so none goes on.
				for _, h := range []string{"If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
					r.Header.Del(h)
				}
				// Per client (reads, then bytes) before install-wide, for the same
				// reason as below.
				tiles.Middleware(tileBytes.MiddlewareN(tileCost)(
					tilesGlobal.Middleware(tileBytesGlobal.MiddlewareN(tileCost)(serve)))).ServeHTTP(w, r)
				return
			}
			// Order matters: a token bucket is charged when it is
			// checked, so every per-client limiter runs BEFORE any
			// install-wide one. Otherwise one noisy client (rejected by
			// its own limit) would still drain the global budget and
			// starve everyone else.
			isSamples := p == "/api/samples"
			isHeavy := demoHeavyPath(p, vehicleID)
			if isHeavy {
				// Before the cache sees the key: a windowed read keeps its normalized window, any other heavy read only tz.
				if demoWindowedPaths[p] {
					demoWindowQuery(r, vehicleID)
				} else {
					demoHeavyQuery(r)
				}
			}
			if isSamples {
				serve = samplesGlobal.Middleware(serve)
			}
			if isHeavy {
				serve = heavyGlobal.Middleware(serve)
			}
			serve = global.Middleware(serve)
			if isSamples {
				serve = samples.Middleware(serve)
			}
			if isHeavy {
				// The cache is outermost of the heavy chain: a hit is served from memory before the per-client and the
				// install-wide heavy budgets, so it spends neither (the general per-client limit above still bounds the request
				// rate). Only a miss, which reads the database, is charged, per client first, then install-wide. Before this, hits
				// drew on the client's budget, and a phone switching tabs and months re-asked for what it already had and was
				// refused (429) on the demo's Activity tab.
				//
				// Compression is negotiated outside the cache, not inside it: the route compresses its own answer (r.With(gz)), and
				// a cache under that kept the compressed bytes of whoever came first with no Content-Encoding, so the next hit sent
				// gzip bytes as JSON (compressed again by an edge that compresses JSON, unreadable to the app) and a client that
				// takes no gzip got them too. The route is asked for plain bytes, the cache keeps those, and each client gets its
				// own encoding on a hit and on a miss alike.
				serve = demoNegotiateEncoding(heavyCache.Middleware(demoPlainDownstream(heavy.Middleware(serve))))
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

// demoRateGlobalPerMin caps the whole anonymous demo regardless of
// client address, so a spoofed or rotating address can't evade the
// per-client limit.
const demoRateGlobalPerMin = 1200

// newDemoLimiter is the per-client limiter; newDemoGlobalLimiter the
// install-wide one. demoAccessMW applies both.
func newDemoLimiter(ls *limiterSet, name string) *ipLimiter {
	l := ls.ip(name, demoRatePerMin, time.Minute, time.Hour)
	l.key = demoClientKey
	l.onDeny = demoDenied("general", "client")
	return l
}

func newDemoGlobalLimiter(ls *limiterSet, name string) *ipLimiter {
	l := ls.ip(name, demoRateGlobalPerMin, time.Minute, time.Hour)
	l.key = func(*http.Request) string { return "global" }
	l.onDeny = demoDenied("general", "global")
	return l
}
