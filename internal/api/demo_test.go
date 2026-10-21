package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

const demoVeh = "demo-0000"
const demoTripID = "0b8c4c3e-3f55-4a56-9c1e-2d6a7a1f9a10"

func TestDemoAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/vehicles", true},
		{"HEAD", "/api/charges", true},
		{"GET", "/api/charges/", true},
		{"GET", "/api/ledger/monthly", true},
		{"GET", "/api/ledger/savings", true},
		{"POST", "/api/ledger/savings", false},
		{"GET", "/api/ledger/savings/x", false},
		{"GET", "/api/ledger/monthly.csv", false},
		{"GET", "/api/settings/charging/offpeak", false},
		{"PUT", "/api/settings/charging/offpeak", false},
		{"GET", "/api/ledger/trips", true},
		{"GET", "/api/trips", false},
		{"GET", "/api/trips/saved", true},
		{"POST", "/api/trips/saved", false},
		{"PUT", "/api/trips/saved/" + demoTripID, false},
		{"DELETE", "/api/trips/saved/" + demoTripID, false},
		{"GET", "/api/trips/plan/diag", false},
		{"GET", "/api/state/" + demoVeh, true},
		{"GET", "/api/vehicles/" + demoVeh + "/now", true},
		{"GET", "/api/charges/clusters", true},
		{"GET", "/api/settings/charging", true},
		{"GET", "/api/settings/home-location", true},
		{"GET", "/api/vehicles/" + demoVeh + "/pack-health", true},
		{"GET", "/api/vehicles/" + demoVeh + "/trends", true},
		{"POST", "/api/vehicles/" + demoVeh + "/trends", false},
		{"PUT", "/api/settings/charging", false},
		{"PUT", "/api/settings/home-location", false},
		{"GET", "/api/settings/charging/networks", true},
		{"PUT", "/api/settings/charging/networks", false},
		{"POST", "/api/settings/charging/networks/reset", false},
		{"GET", "/api/settings/charging/networks/extra", false},
		// demo parity: drain (demo vehicle only), saved-trip actual,
		// charger archive tiles
		{"GET", "/api/vehicles/" + demoVeh + "/drain", true},
		{"GET", "/api/vehicles/other/drain", false},
		{"POST", "/api/vehicles/" + demoVeh + "/drain", false},
		{"GET", "/api/trips/saved/" + demoTripID + "/actual", true},
		{"GET", "/api/trips/saved/not-a-uuid/actual", false},
		{"GET", "/api/trips/saved/../actual", false},
		{"GET", "/api/trips/saved/" + demoTripID, false},
		{"GET", "/api/trips/saved/" + demoTripID + "/actual/x", false},
		{"POST", "/api/trips/saved/" + demoTripID + "/link", false},
		{"POST", "/api/trips/saved/" + demoTripID + "/unlink", false},
		{"GET", "/api/maps/tiles/chargers.pmtiles", true},
		{"GET", "/api/maps/tiles/other.pmtiles", false},
		{"GET", "/api/maps/tiles/chargers.pmtiles.bak", false},
		// never reachable: planner compute, AI, geocode, routing, corridor
		{"POST", "/api/trips/plan", false},
		{"POST", "/api/trips/plan-multiday", false},
		{"POST", "/api/trips/plan/advice", false},
		{"POST", "/api/trips/plan/raw", false},
		{"GET", "/api/trips/plan/diag", false},
		{"GET", "/api/geocode", false},
		{"GET", "/api/maps/valhalla/route", false},
		{"POST", "/api/maps/valhalla/route", false},
		{"POST", "/api/maps/chargers-along", false},
		{"GET", "/api/maps/chargers-along", false},
		{"GET", "/api/live-drive", false},
		{"GET", "/api/vehicles/other/pack-health", false},
		{"GET", "/api/vehicles/other/trends", false},
		// per-drive detail (weather only) and samples
		{"GET", "/api/drives/drive-abc/weather", true},
		{"GET", "/api/drives/live_veh_d_1760000000/weather/series", true},
		{"GET", "/api/samples", true},
		{"HEAD", "/api/drives/d1/weather", true},
		{"GET", "/api/drives/d1/efficiency", false},
		{"POST", "/api/drives/d1/weather", false},
		{"POST", "/api/drives/weather/backfill", false},
		{"GET", "/api/drives//weather", false},
		{"GET", "/api/drives/a/b/weather", false},
		{"GET", "/api/drives/../charges/weather", false},
		{"GET", "/api/drives/d1/weather/series/extra", false},
		{"GET", "/api/drives/electrafi_ab12cd_d_1700000000", true}, // one drive with its polyline (the demo list serves the same row)
		{"GET", "/api/drives/d1", false},                           // not a drive id: a future /drives/<word> endpoint must not join the allowlist
		{"GET", "/api/drives/export", false},
		{"GET", "/api/drives/live_veh_d_", false},
		{"GET", "/api/drives/live_veh_d_17x", false},
		{"GET", "/api/drives/other_veh_d_1760000000", false},
		{"GET", "/api/drives/live_veh_d_1760000000", true},
		{"GET", "/api/drives/a%20b", false},
		{"POST", "/api/drives/live_veh_d_1760000000", false},
		{"DELETE", "/api/drives/live_veh_d_1760000000", false},
		{"GET", "/api/drives/d%2F1/weather", false},
		{"DELETE", "/api/samples", false},
		// other vehicles
		{"GET", "/api/state/other", false},
		{"GET", "/api/vehicles/other/now", false},
		{"GET", "/api/state/" + demoVeh + "/debug", false},
		{"GET", "/api/state/" + demoVeh + "/fresh", false},
		// writes and non-allowlisted reads
		{"POST", "/api/charges", false},
		{"PATCH", "/api/charges/x/pricing", false},
		{"DELETE", "/api/charges/x", false},
		{"PUT", "/api/settings/charging", false},
		{"GET", "/api/admin/users", false},
		{"GET", "/api/auth/me", false},
		{"GET", "/api/vehicles/owned", false},
		{"GET", "/api/ledger/export", false},
		{"GET", "/api/ledger/../admin/users", false},
	}
	for _, c := range cases {
		if got := demoAllowed(c.method, c.path, demoVeh); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func demoChain(limiter *ipLimiter, real *uuid.UUID) (http.Handler, uuid.UUID) {
	demoUID := uuid.New()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := auth.UserFromContext(r.Context()); ok {
			w.Header().Set("X-Test-User", u.String())
		}
		w.WriteHeader(http.StatusOK)
	})
	h := demoAccessMW(demoUID, demoVeh, limiter, newIPLimiter(1000, time.Minute, time.Hour))(inner)
	if real != nil {
		// Simulate a session resolved by the auth middleware upstream.
		h = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), *real)))
			})
		}(h)
	}
	return h, demoUID
}

func demoReq(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.9:4000"
	if strings.Contains(path, "/tiles/") {
		req.Header.Set("Range", "bytes=0-16383") // tiles need a bounded range
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestDemoAccessMW_AnonymousAllowlistedBecomesDemoUser(t *testing.T) {
	h, uid := demoChain(newDemoLimiter(), nil)
	rec := demoReq(h, "GET", "/api/charges")
	if rec.Header().Get("X-Test-User") != uid.String() || rec.Header().Get(demoHeader) != "1" {
		t.Fatalf("anonymous allowlisted GET not served as demo: %v", rec.Header())
	}
	for _, c := range [][2]string{
		{"POST", "/api/charges"},
		{"GET", "/api/admin/users"},
		{"GET", "/api/state/other"},
		{"PUT", "/api/settings/charging/networks"},
		{"POST", "/api/trips/plan"},
		{"GET", "/api/geocode"},
	} {
		rec := demoReq(h, c[0], c[1])
		if rec.Header().Get("X-Test-User") != "" || rec.Header().Get(demoHeader) != "" {
			t.Errorf("%s %s got demo identity", c[0], c[1])
		}
	}
}

func TestDemoAccessMW_SignedInUserIsNeverSwitched(t *testing.T) {
	real := uuid.New()
	h, _ := demoChain(newDemoLimiter(), &real)
	rec := demoReq(h, "GET", "/api/charges")
	if rec.Header().Get("X-Test-User") != real.String() || rec.Header().Get(demoHeader) != "" {
		t.Fatalf("signed-in user was switched: %v", rec.Header())
	}
}

func TestDemoAccessMW_RateLimited(t *testing.T) {
	h, _ := demoChain(newIPLimiter(3, time.Minute, time.Hour), nil)
	codes := []int{}
	for i := 0; i < 5; i++ {
		codes = append(codes, demoReq(h, "GET", "/api/charges").Code)
	}
	if codes[2] != 200 || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
	// Non-allowlisted requests don't consume demo budget.
	if demoReq(h, "GET", "/api/admin/users").Code != 200 {
		t.Fatal("non-demo request was rate limited by the demo limiter")
	}
}

func TestDemoConfigFlagOffByDefault(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	rec := httptest.NewRecorder()
	handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest("GET", "/api/config", nil))
	if !contains(rec.Body.String(), `"demo_enabled":false`) {
		t.Fatalf("demo_enabled should be false: %s", rec.Body)
	}
	t.Setenv("RIVOLT_DEMO", "true")
	rec = httptest.NewRecorder()
	handleConfig(false, false, false, false, nil, nil)(rec, httptest.NewRequest("GET", "/api/config", nil))
	if !contains(rec.Body.String(), `"demo_enabled":true`) {
		t.Fatalf("demo_enabled should be true: %s", rec.Body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDemoRateLimitKeyedOnCFConnectingIP(t *testing.T) {
	l := newDemoLimiter()
	l.rps, l.burst = 0, 2 // two requests per key, no refill
	h, _ := demoChain(l, nil)
	do := func(cf, spoof string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/charges", nil)
		req.RemoteAddr = "173.245.48.5:1" // a Cloudflare edge address
		req.Header.Set("CF-Connecting-IP", cf)
		req.Header.Set("X-Real-IP", spoof) // client-controlled; must not matter
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if do("198.51.100.1", "1.1.1.1") != 200 || do("198.51.100.1", "2.2.2.2") != 200 {
		t.Fatal("first two should pass")
	}
	if got := do("198.51.100.1", "3.3.3.3"); got != http.StatusTooManyRequests {
		t.Fatalf("spoofing X-Real-IP evaded the limit: %d", got)
	}
	if do("198.51.100.2", "3.3.3.3") != 200 {
		t.Fatal("a different client IP has its own bucket")
	}
}

func TestDemoGlobalLimiter(t *testing.T) {
	g := newDemoGlobalLimiter()
	g.rps, g.burst = 0, 2
	demoUID := uuid.New()
	h := demoAccessMW(demoUID, demoVeh, newIPLimiter(1000, time.Minute, time.Hour), g)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	codes := []int{}
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/charges", nil)
		req.Header.Set("CF-Connecting-IP", "198.51.100."+string(rune('1'+i)))
		req.RemoteAddr = "173.245.48.5:1"
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("global cap should bind across client IPs: %v", codes)
	}
}

func TestDemoSamplesLimitIsClamped(t *testing.T) {
	var seen string
	demoUID := uuid.New()
	h := demoAccessMW(demoUID, demoVeh, newIPLimiter(1000, time.Minute, time.Hour), newIPLimiter(1000, time.Minute, time.Hour))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.URL.Query().Get("limit") }))
	for _, c := range []struct{ q, want string }{
		{"", "2000"},
		{"?limit=0", "2000"},
		{"?limit=-5", "2000"},
		{"?limit=100", "100"},
		{"?limit=999999999", "5000"},
		{"?limit=abc&since=2026-10-01T00:00:00Z", "2000"},
	} {
		seen = ""
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/samples"+c.q, nil)
		req.RemoteAddr = "203.0.113.9:1"
		h.ServeHTTP(rec, req)
		if seen != c.want {
			t.Errorf("%q: limit = %q, want %q", c.q, seen, c.want)
		}
	}
}

// /api/samples has its own anonymous budget, separate from the general
// demo limits and from other demo endpoints.
func TestDemoSamplesHasItsOwnTighterLimits(t *testing.T) {
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	do := func(path, ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("CF-Connecting-IP", ip)
		req.RemoteAddr = "173.245.48.5:1"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// Per client: 20 pass, the 21st is 429; other endpoints are unaffected.
	for i := 0; i < demoSamplesPerClientPerMin; i++ {
		if c := do("/api/samples", "198.51.100.1"); c != 200 {
			t.Fatalf("request %d = %d", i+1, c)
		}
	}
	if c := do("/api/samples", "198.51.100.1"); c != http.StatusTooManyRequests {
		t.Fatalf("21st samples request = %d, want 429", c)
	}
	if c := do("/api/charges", "198.51.100.1"); c != 200 {
		t.Fatalf("other endpoint throttled by the samples budget: %d", c)
	}
	// Another client has its own per-client budget.
	if c := do("/api/samples", "198.51.100.2"); c != 200 {
		t.Fatalf("different client = %d", c)
	}
	// Install-wide: 200 per minute across clients (already used 21).
	got429 := false
	for i := 0; i < demoSamplesGlobalPerMin; i++ {
		ip := "203.0.113." + strconv.Itoa(i%250+1) + "." + strconv.Itoa(i)
		if do("/api/samples", ip) == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("global samples cap never bound")
	}
}

// A noisy client rejected by its own per-client limit must not drain
// the install-wide budget: the per-client limiter is checked first.
func TestDemoNoisyClientDoesNotExhaustGlobalBudget(t *testing.T) {
	perClient := newIPLimiter(5, time.Minute, time.Hour)
	perClient.key = demoClientKey
	global := newIPLimiter(10, time.Minute, time.Hour)
	global.key = func(*http.Request) string { return "global" }
	h := demoAccessMW(uuid.New(), demoVeh, perClient, global)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	do := func(path, ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("CF-Connecting-IP", ip)
		req.RemoteAddr = "173.245.48.5:1"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// 100 requests from one client: 5 served, 95 rejected by its own limit.
	served := 0
	for i := 0; i < 100; i++ {
		if do("/api/charges", "198.51.100.7") == 200 {
			served++
		}
	}
	if served != 5 {
		t.Fatalf("noisy client served %d, want 5", served)
	}
	// The global budget (10) was charged 5 times, not 100: five other
	// clients still get through.
	for i := 0; i < 5; i++ {
		if c := do("/api/charges", "203.0.113."+strconv.Itoa(i+1)); c != 200 {
			t.Fatalf("other client %d = %d: the noisy client drained the global budget", i, c)
		}
	}
}

func TestDemoNoisySamplesClientDoesNotExhaustGlobalSamplesBudget(t *testing.T) {
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	do := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/samples", nil)
		req.Header.Set("CF-Connecting-IP", ip)
		req.RemoteAddr = "173.245.48.5:1"
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	ok := 0
	for i := 0; i < 1000; i++ {
		if do("198.51.100.9") == 200 {
			ok++
		}
	}
	if ok != demoSamplesPerClientPerMin {
		t.Fatalf("noisy client served %d, want %d", ok, demoSamplesPerClientPerMin)
	}
	// It used 20 of the 200 install-wide, not 1000: 150 other clients
	// (well under the remaining 180) all pass.
	for i := 0; i < 150; i++ {
		if c := do("203.0.113." + strconv.Itoa(i+1)); c != 200 {
			t.Fatalf("client %d = %d: the noisy client drained the global samples budget", i, c)
		}
	}
}

func TestDemoTiles_OnlyPublicArchivesAnonymousAndOwnBudget(t *testing.T) {
	h, uid := demoChain(newDemoLimiter(), nil)
	for _, c := range []struct {
		method, path string
		ok           bool
	}{
		{"GET", "/api/maps/tiles/us.pmtiles", true},
		{"HEAD", "/api/maps/tiles/us.pmtiles", true},
		{"POST", "/api/maps/tiles/us.pmtiles", false},
		// Demo parity: the public charger archive is readable too (same
		// tile budgets); nothing else under /api/maps/tiles is.
		{"GET", "/api/maps/tiles/chargers.pmtiles", true},
		{"GET", "/api/maps/tiles/other.pmtiles", false},
		{"GET", "/api/maps/tiles/us.pmtiles/../chargers.pmtiles", false},
		{"GET", "/api/maps/valhalla/trace_route", false},
		{"POST", "/api/maps/valhalla/route", false},
		{"POST", "/api/maps/chargers-along", false},
	} {
		rec := demoReq(h, c.method, c.path)
		isDemo := rec.Header().Get("X-Test-User") == uid.String() && rec.Header().Get(demoHeader) == "1"
		if isDemo != c.ok {
			t.Errorf("%s %s: served as demo = %v, want %v", c.method, c.path, isDemo, c.ok)
		}
	}
}

func TestDemoTiles_DoNotDrawOnGeneralBudget(t *testing.T) {
	// General per-client budget of 2: hammer tiles, then data still works.
	h, _ := demoChain(newIPLimiter(2, time.Minute, time.Hour), nil)
	for i := 0; i < 50; i++ {
		if rec := demoReq(h, "GET", "/api/maps/tiles/us.pmtiles"); rec.Code != http.StatusOK {
			t.Fatalf("tile %d: %d", i, rec.Code)
		}
	}
	if rec := demoReq(h, "GET", "/api/charges"); rec.Code != http.StatusOK {
		t.Fatalf("data call starved by tiles: %d", rec.Code)
	}
}

func TestDemoTiles_PerClientLimit(t *testing.T) {
	h, _ := demoChain(newDemoLimiter(), nil)
	limited := false
	for i := 0; i < demoTilesPerClientPerMin+50; i++ {
		if demoReq(h, "GET", "/api/maps/tiles/us.pmtiles").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("tiles must be rate limited per client")
	}
}

func TestClampDemoSamplesSpan(t *testing.T) {
	until := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct{ name, since, wantSince string }{
		{"wide window clamped", until.Add(-30 * 24 * time.Hour).Format(time.RFC3339), until.Add(-24 * time.Hour).Format(time.RFC3339)},
		{"missing since bounded", "", until.Add(-24 * time.Hour).Format(time.RFC3339)},
		{"narrow window kept", until.Add(-14 * time.Hour).Format(time.RFC3339), until.Add(-14 * time.Hour).Format(time.RFC3339)},
	}
	for _, c := range cases {
		q := url.Values{"until": {until.Format(time.RFC3339)}}
		if c.since != "" {
			q.Set("since", c.since)
		}
		r := httptest.NewRequest("GET", "/api/samples?"+q.Encode(), nil)
		clampDemoSamplesLimit(r)
		if got := r.URL.Query().Get("since"); got != c.wantSince {
			t.Errorf("%s: since = %q, want %q", c.name, got, c.wantSince)
		}
	}
}

func TestDemoDrainLimitAndCache(t *testing.T) {
	demoUID := uuid.New()
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	h := demoAccessMW(demoUID, demoVeh, newIPLimiter(1000, time.Minute, time.Hour), newIPLimiter(1000, time.Minute, time.Hour))(inner)
	path := "/api/vehicles/" + demoVeh + "/drain"
	var codes []int
	for i := 0; i < demoHeavyPerClientPerMin+3; i++ {
		rec := demoReq(h, "GET", path)
		codes = append(codes, rec.Code)
		if rec.Code == 200 && (rec.Header().Get(demoHeader) != "1" || rec.Body.String() != `{"ok":true}`) {
			t.Fatalf("response %d: header %q body %q", i, rec.Header().Get(demoHeader), rec.Body.String())
		}
	}
	for i, c := range codes {
		want := 200
		if i >= demoHeavyPerClientPerMin {
			want = 429
		}
		if c != want {
			t.Fatalf("codes = %v, want 200s then 429s", codes)
		}
	}
	// Ten identical reads inside the TTL reached the handler once.
	if calls != 1 {
		t.Errorf("handler called %d times, want 1 (cached)", calls)
	}
}

func TestDemoResponseCacheExpiresAndSkipsErrors(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := newDemoResponseCache(time.Minute, 2)
	c.now = func() time.Time { return now }
	status, calls := 200, 0
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte("x"))
	}))
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/p?"+q, nil))
		return rec
	}
	get("a=1")
	if rec := get("a=1"); calls != 1 || rec.Header().Get("X-Rivolt-Demo-Cache") != "hit" {
		t.Fatalf("second read not cached: calls %d", calls)
	}
	now = now.Add(2 * time.Minute)
	get("a=1")
	if calls != 2 {
		t.Errorf("entry did not expire: calls %d", calls)
	}
	// Errors are never cached; a full cache serves new keys uncached.
	status = 500
	get("b=1")
	get("b=1")
	if calls != 4 {
		t.Errorf("500 was cached: calls %d", calls)
	}
	status = 200
	get("c=1")
	get("d=1") // third distinct key, cache max 2
	before := calls
	get("d=1")
	if calls != before+1 {
		t.Errorf("overflow key was cached: calls %d vs %d", calls, before)
	}
}

func TestDemoClientKeyTrustsCFHeaderOnlyFromCloudflare(t *testing.T) {
	key := func(remote, xff, cf string) string {
		req := httptest.NewRequest("GET", "/api/charges", nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		if cf != "" {
			req.Header.Set("CF-Connecting-IP", cf)
		}
		return demoClientKey(req)
	}
	// Peer is a Cloudflare edge (the last X-Forwarded-For entry): the
	// visitor address in CF-Connecting-IP is used.
	if got := key("10.42.0.7:1", "173.245.48.5", "198.51.100.8"); got != "198.51.100.8" {
		t.Errorf("cloudflare peer: %q", got)
	}
	if got := key("10.42.0.7:1", "9.9.9.9, 2606:4700::1", "198.51.100.8"); got != "198.51.100.8" {
		t.Errorf("cloudflare v6 peer: %q", got)
	}
	// Peer is not Cloudflare: the header is ignored, so spoofing it does
	// not give a fresh bucket.
	if got := key("10.42.0.7:1", "203.0.113.50", "198.51.100.8"); got != "203.0.113.50" {
		t.Errorf("non-cloudflare peer trusted the header: %q", got)
	}
	// A client-supplied XFF entry on the left cannot make a non-CF peer
	// look like Cloudflare.
	if got := key("10.42.0.7:1", "173.245.48.5, 203.0.113.50", "198.51.100.8"); got != "203.0.113.50" {
		t.Errorf("forged left XFF entry: %q", got)
	}
	// No proxy header at all: the connection address, header ignored.
	if got := key("203.0.113.9:4000", "", "198.51.100.8"); got != "203.0.113.9" {
		t.Errorf("direct: %q", got)
	}
	// Our own load balancer hop (private peer, as svclb/Traefik report
	// it): the header is honoured, from XFF or the connection address.
	if got := key("10.42.0.7:1", "10.42.0.172", "198.51.100.8"); got != "198.51.100.8" {
		t.Errorf("private lb peer via xff: %q", got)
	}
	if got := key("10.42.0.172:1", "", "198.51.100.8"); got != "198.51.100.8" {
		t.Errorf("private lb peer direct: %q", got)
	}
	if got := key("10.42.0.172:1", "", ""); got != "10.42.0.172" {
		t.Errorf("private peer, no header: %q", got)
	}
	// A public non-Cloudflare peer is still not believed.
	if got := key("10.42.0.172:1", "198.51.100.99", "198.51.100.8"); got != "198.51.100.99" {
		t.Errorf("public non-cf peer: %q", got)
	}
	// Cloudflare peer but a garbage CF-Connecting-IP: fall back to the peer.
	if got := key("10.42.0.7:1", "173.245.48.5", "not-an-ip"); got != "173.245.48.5" {
		t.Errorf("bad cf header: %q", got)
	}
}

func TestDemoTileRangeGuard(t *testing.T) {
	h, _ := demoChain(newDemoLimiter(), nil)
	do := func(method, rng string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/maps/tiles/us.pmtiles", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, c := range []struct {
		method, rng string
		want        int
	}{
		{"GET", "", 400},                      // no range: the whole archive
		{"GET", "bytes=0-16383", 200},         // pmtiles header read
		{"GET", "bytes=1048576-3145727", 200}, // exactly 2 MiB
		{"GET", "bytes=0-2097152", 416},       // one byte over
		{"GET", "bytes=0-", 400},              // open ended
		{"GET", "bytes=-1024", 200},           // suffix within cap
		{"GET", "bytes=-9999999", 416},        // suffix over cap
		{"GET", "bytes=0-1,5-9", 400},         // multi-range
		{"GET", "bytes=9-1", 400},
		{"GET", "items=0-1", 400},
		{"GET", "bytes=a-b", 400},
		{"HEAD", "", 200}, // no body, no range needed
	} {
		if got := do(c.method, c.rng); got != c.want {
			t.Errorf("%s Range %q = %d, want %d", c.method, c.rng, got, c.want)
		}
	}
}

func TestDemoResponsesAreNotCacheableByShares(t *testing.T) {
	h, _ := demoChain(newDemoLimiter(), nil)
	rec := demoReq(h, "GET", "/api/charges")
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	vary := strings.Join(rec.Header().Values("Vary"), ",")
	if !strings.Contains(vary, "Cookie") || !strings.Contains(vary, "Authorization") {
		t.Errorf("Vary = %q", vary)
	}
	// Tiles are public archives, not demo-user data: left cacheable.
	if rec := demoReq(h, "GET", "/api/maps/tiles/us.pmtiles"); rec.Header().Get("Cache-Control") != "" {
		t.Errorf("tile Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestDemoHeavyReadsIgnoreQueryNoise(t *testing.T) {
	var seen []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		_, _ = w.Write([]byte("{}"))
	})
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(inner)
	drain := "/api/vehicles/" + demoVeh + "/drain"
	// Random tz values and extra params all collapse to one cache entry.
	for _, q := range []string{"", "?tz=Mars/Base", "?tz=random1&x=1", "?foo=bar", "?tz="} {
		if rec := demoReq(h, "GET", drain+q); rec.Code != 200 {
			t.Fatalf("%q = %d", q, rec.Code)
		}
	}
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("handler saw %q, want one call with no query", seen)
	}
	// An allowed zone passes through (and is its own cache key).
	demoReq(h, "GET", drain+"?tz=America/Chicago&junk=1")
	demoReq(h, "GET", drain+"?tz=America/Chicago")
	if len(seen) != 2 || seen[1] != "tz=America%2FChicago" {
		t.Fatalf("allowed tz: %q", seen)
	}
}

func TestDemoPlanActualSharesHeavyBudgetAndCache(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("{}"))
	})
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(inner)
	path := "/api/trips/saved/" + demoTripID + "/actual"
	var codes []int
	for i := 0; i < demoHeavyPerClientPerMin+2; i++ {
		codes = append(codes, demoReq(h, "GET", path+"?cachebust="+strconv.Itoa(i)).Code)
	}
	if codes[demoHeavyPerClientPerMin-1] != 200 || codes[demoHeavyPerClientPerMin] != 429 {
		t.Fatalf("codes = %v", codes)
	}
	if calls != 1 {
		t.Errorf("cache-busting params reached the handler %d times", calls)
	}
}

func TestDemoResponseCacheSingleflight(t *testing.T) {
	c := newDemoResponseCache(time.Minute, 4)
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var mu sync.Mutex
	calls := 0
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	var wg sync.WaitGroup
	bodies := make([]string, 6)
	for i := range bodies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/p", nil))
			bodies[i] = rec.Body.String()
		}(i)
	}
	<-started // the leader is computing
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls != 1 {
		t.Errorf("handler ran %d times for 6 concurrent requests, want 1", calls)
	}
	for i, b := range bodies {
		if b != "ok" {
			t.Errorf("body %d = %q", i, b)
		}
	}
}

func TestDemoHeavyCacheHitsDoNotSpendTheSharedBudget(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("{}"))
	})
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(inner)
	path := "/api/vehicles/" + demoVeh + "/drain"
	// 3 x the shared per-minute budget of requests, each from its own
	// client (so no per-client limit binds): one miss, the rest cache hits.
	n := demoHeavyGlobalPerMin * 3
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "173.245.48.5:1" // Cloudflare edge
		req.Header.Set("CF-Connecting-IP", "198.51."+strconv.Itoa(i/250)+"."+strconv.Itoa(i%250+1))
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("request %d = %d: cache hits spent the shared budget", i, rec.Code)
		}
	}
	if calls != 1 {
		t.Errorf("handler ran %d times, want 1", calls)
	}
}

func TestDemoClientKeyObserverCountsSource(t *testing.T) {
	var got []string
	demoClientKeyObserver = func(s string) { got = append(got, s) }
	t.Cleanup(func() { demoClientKeyObserver = nil })
	mk := func(remote, cf string) {
		req := httptest.NewRequest("GET", "/api/charges", nil)
		req.RemoteAddr = remote
		if cf != "" {
			req.Header.Set("CF-Connecting-IP", cf)
		}
		demoClientKey(req)
	}
	mk("173.245.48.5:1", "198.51.100.8")
	mk("203.0.113.9:1", "198.51.100.8")
	mk("173.245.48.5:1", "garbage")
	mk("10.42.0.172:1", "198.51.100.8")
	if len(got) != 4 || got[0] != "cf" || got[1] != "peer" || got[2] != "peer" || got[3] != "lb" {
		t.Errorf("sources = %v", got)
	}
}

// The tile budgets come from the env when it holds a sane whole number,
// else the defaults; the limiter enforces what was read.
func TestDemoTileBudgetsFromEnv(t *testing.T) {
	for _, c := range []struct {
		client, global       string
		wantClient, wantGlob int
	}{
		{"", "", demoTilesPerClientPerMin, demoTilesGlobalPerMin},
		{"600", "3000", 600, 3000},
		{" 600 ", "3000", 600, 3000},
		{"0", "-5", demoTilesPerClientPerMin, demoTilesGlobalPerMin},
		{"lots", "3e3", demoTilesPerClientPerMin, demoTilesGlobalPerMin},
		{"100001", "100000", demoTilesPerClientPerMin, 100000},
	} {
		t.Setenv("RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN", c.client)
		t.Setenv("RIVOLT_DEMO_TILES_GLOBAL_PER_MIN", c.global)
		if pc, g := demoTileBudgets(); pc != c.wantClient || g != c.wantGlob {
			t.Errorf("env %q/%q: %d/%d, want %d/%d", c.client, c.global, pc, g, c.wantClient, c.wantGlob)
		}
	}

	tilesUntil429 := func(t *testing.T) int {
		t.Helper()
		h, _ := demoChain(newDemoLimiter(), nil)
		for i := 1; i <= 200; i++ {
			if demoReq(h, "GET", "/api/maps/tiles/us.pmtiles").Code == http.StatusTooManyRequests {
				return i
			}
		}
		return 0
	}
	t.Setenv("RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN", "5")
	t.Setenv("RIVOLT_DEMO_TILES_GLOBAL_PER_MIN", "")
	if n := tilesUntil429(t); n != 6 {
		t.Errorf("per-client 5: first 429 at request %d, want 6", n)
	}
	t.Setenv("RIVOLT_DEMO_TILES_PER_CLIENT_PER_MIN", "100")
	t.Setenv("RIVOLT_DEMO_TILES_GLOBAL_PER_MIN", "3")
	if n := tilesUntil429(t); n != 4 {
		t.Errorf("global 3: first 429 at request %d, want 4", n)
	}
}

func TestDemoRateLimitedObserverCountsByClassAndScope(t *testing.T) {
	type k struct{ class, scope string }
	got := map[k]int{}
	demoRateLimitedObserver = func(c, s string) { got[k{c, s}]++ }
	t.Cleanup(func() { demoRateLimitedObserver = nil })

	// general client limit of 1
	l := newDemoLimiter()
	l.rps, l.burst = 0, 1
	h, _ := demoChain(l, nil)
	demoReq(h, "GET", "/api/charges")
	demoReq(h, "GET", "/api/charges")
	// samples: 20/min per client, then 429
	h2, _ := demoChain(newIPLimiter(100000, time.Minute, time.Hour), nil)
	for i := 0; i < demoSamplesPerClientPerMin+2; i++ {
		demoReq(h2, "GET", "/api/samples")
	}
	// heavy: 10/min per client, then 429
	for i := 0; i < demoHeavyPerClientPerMin+2; i++ {
		demoReq(h2, "GET", "/api/vehicles/"+demoVeh+"/drain")
	}
	if got[k{"general", "client"}] != 1 {
		t.Errorf("general/client = %d", got[k{"general", "client"}])
	}
	if got[k{"samples", "client"}] != 2 {
		t.Errorf("samples/client = %d", got[k{"samples", "client"}])
	}
	if got[k{"heavy", "client"}] != 2 {
		t.Errorf("heavy/client = %d", got[k{"heavy", "client"}])
	}
}

// Through the Cloudflare tunnel the rightmost X-Forwarded-For entry is the
// visitor (public, outside Cloudflare's edge ranges): a per-visitor key
// counted as "xff", not "peer".
func TestDemoClientKeyObserverClassifiesTunnelVisitorsAsXFF(t *testing.T) {
	var got []string
	demoClientKeyObserver = func(s string) { got = append(got, s) }
	t.Cleanup(func() { demoClientKeyObserver = nil })
	key := func(remote, xff, cf string) string {
		req := httptest.NewRequest("GET", "/api/charges", nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		if cf != "" {
			req.Header.Set("CF-Connecting-IP", cf)
		}
		return demoClientKey(req)
	}
	// Tunnel: cloudflared (private) is the connection peer, the visitor is
	// the rightmost XFF entry, and there may be no CF-Connecting-IP.
	if k := key("10.42.0.9:1", "203.0.113.50", ""); k != "203.0.113.50" {
		t.Errorf("tunnel visitor key = %q", k)
	}
	// A forged left entry changes nothing: still the rightmost one.
	if k := key("10.42.0.9:1", "1.1.1.1, 203.0.113.51", ""); k != "203.0.113.51" {
		t.Errorf("forged left XFF changed the key: %q", k)
	}
	// A public non-Cloudflare XFF peer with a spoofed CF header: still refused.
	if k := key("10.42.0.9:1", "203.0.113.52", "198.51.100.1"); k != "203.0.113.52" {
		t.Errorf("spoofed CF header honoured: %q", k)
	}
	// No XFF at all: the connection address, counted as peer.
	if k := key("203.0.113.9:4000", "", ""); k != "203.0.113.9" {
		t.Errorf("direct key = %q", k)
	}
	want := []string{"xff", "xff", "xff", "peer"}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sources = %v, want %v", got, want)
			break
		}
	}
}

// The savings estimate is a heavy anonymous read: any month, months or noise in
// the query collapses to one cached answer (the last complete month), and an
// allowed zone is its own key.
func TestDemoSavingsIgnoresQueryNoise(t *testing.T) {
	var seen []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery)
		_, _ = w.Write([]byte("{}"))
	})
	big := func() *ipLimiter { return newIPLimiter(100000, time.Minute, time.Hour) }
	h := demoAccessMW(uuid.New(), demoVeh, big(), big())(inner)
	for _, q := range []string{"", "?months=12", "?month=2020-01&months=12", "?vehicle_id=other", "?tz=Mars/Base"} {
		if rec := demoReq(h, "GET", "/api/ledger/savings"+q); rec.Code != 200 {
			t.Fatalf("%q = %d", q, rec.Code)
		}
	}
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("handler saw %q, want one call with no query", seen)
	}
}
