package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
)

const demoVeh = "demo-0000"

func TestDemoAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/vehicles", true},
		{"HEAD", "/api/charges", true},
		{"GET", "/api/charges/", true},
		{"GET", "/api/ledger/monthly", true},
		{"GET", "/api/ledger/trips", true},
		{"GET", "/api/trips", false},
		{"GET", "/api/trips/saved", false},
		{"GET", "/api/trips/plan/diag", false},
		{"GET", "/api/state/" + demoVeh, true},
		{"GET", "/api/vehicles/" + demoVeh + "/now", true},
		{"GET", "/api/charges/clusters", true},
		{"GET", "/api/settings/charging", true},
		{"GET", "/api/settings/home-location", true},
		{"GET", "/api/vehicles/" + demoVeh + "/pack-health", true},
		{"PUT", "/api/settings/charging", false},
		{"PUT", "/api/settings/home-location", false},
		{"GET", "/api/settings/charging/networks", false},
		{"POST", "/api/settings/charging/networks/reset", false},
		{"GET", "/api/vehicles/other/pack-health", false},
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
		{"GET", "/api/drives/d1", false},
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
		{"GET", "/api/settings/charging/networks"},
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
		req.RemoteAddr = "10.0.0.1:1"
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

func TestDemoTiles_OnlyBasemapAnonymousAndOwnBudget(t *testing.T) {
	h, uid := demoChain(newDemoLimiter(), nil)
	for _, c := range []struct {
		method, path string
		ok           bool
	}{
		{"GET", "/api/maps/tiles/us.pmtiles", true},
		{"HEAD", "/api/maps/tiles/us.pmtiles", true},
		{"POST", "/api/maps/tiles/us.pmtiles", false},
		{"GET", "/api/maps/tiles/chargers.pmtiles", false},
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
