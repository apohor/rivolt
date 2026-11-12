package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/radar"
)

// demoRadarRig is the real router with the demo on, a demo radar over a counting upstream, and no session on any request.
type demoRadarRig struct {
	router  http.Handler
	m       *metrics.Metrics
	nearby  string         // a tile path inside the demo's set
	hits    atomic.Int32   // upstream requests of any kind (capabilities or tile)
	results map[string]int // the hit/miss counts the demo reported
}

func newDemoRadarRig(t *testing.T) *demoRadarRig {
	t.Helper()
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	rig := &demoRadarRig{m: metrics.New(), results: map[string]int{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.hits.Add(1)
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(up.Close)
	box := radar.Box{W: -116, S: 35, E: -114.5, N: 36.5}
	d := radar.NewDemo(radar.New(radar.Config{BaseURL: up.URL}), []radar.Box{box}, func(r string) {
		rig.results[r]++
		rig.m.DemoRadarTotal.WithLabelValues(r).Inc()
	}, nil)
	for x := 0; x < 32 && rig.nearby == ""; x++ {
		for y := 0; y < 32; y++ {
			if d.In(5, x, y) {
				rig.nearby = "/api/demo/radar/5/" + strconv.Itoa(x) + "/" + strconv.Itoa(y) + ".png"
				break
			}
		}
	}
	if rig.nearby == "" {
		t.Fatal("no tile in the demo set")
	}
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	rig.router = New(Deps{Auth: svc, AuthEnforced: true, DemoRadar: d.Handler(), Metrics: rig.m})
	return rig
}

func (g *demoRadarRig) get(path, client string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
	req.RemoteAddr = client + ":4000"
	g.router.ServeHTTP(rec, req)
	return rec
}

// Signed out, inside the sample trips: a tile is answered (an empty one before the warm loop has stored it), with nothing user-specific and not one
// upstream request. Outside the set: 404, still free.
func TestDemoRadarSignedOutInsideAndOutside(t *testing.T) {
	g := newDemoRadarRig(t)
	rec := g.get(g.nearby+"?t=1700000000", "203.0.113.9")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("inside: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "public, max-age=") || strings.Contains(cc, "private") {
		t.Errorf("Cache-Control %q", cc)
	}
	if rec.Header().Get("Set-Cookie") != "" || strings.Contains(strings.ToLower(rec.Header().Get("Vary")), "cookie") {
		t.Errorf("per-visitor headers: %v", rec.Header())
	}
	if rec := g.get("/api/demo/radar/5/0/0.png", "203.0.113.9"); rec.Code != 404 {
		t.Errorf("outside the set: %d", rec.Code)
	}
	if rec := g.get("/api/demo/radar/frame", "203.0.113.9"); rec.Code != 503 {
		t.Errorf("frame before the first warm: %d", rec.Code)
	}
	if n := g.hits.Load(); n != 0 {
		t.Errorf("%d upstream requests from demo requests", n)
	}
	if g.results["miss"] != 1 || testutil.ToFloat64(g.m.DemoRadarTotal.WithLabelValues("miss")) != 1 {
		t.Errorf("counts %v", g.results)
	}
}

// Past the per-client budget a visitor gets 429 (counted as limited, without reaching the radar); another visitor is unaffected. The bucket
// refills a little while the loop runs, so the counts are checked against each other, not against exact numbers.
func TestDemoRadarPerClientLimit(t *testing.T) {
	g := newDemoRadarRig(t)
	answered, limited := 0, 0
	for i := 0; i < demoRadarPerClientPerMin+200; i++ {
		if g.get(g.nearby, "203.0.113.20").Code == http.StatusTooManyRequests {
			limited++
		} else {
			answered++
		}
	}
	if answered < demoRadarPerClientPerMin || limited == 0 {
		t.Errorf("answered %d limited %d, want at least %d answered and some limited", answered, limited, demoRadarPerClientPerMin)
	}
	if g.results["miss"] != answered {
		t.Errorf("the radar answered %d, the router %d", g.results["miss"], answered)
	}
	if got := testutil.ToFloat64(g.m.DemoRadarTotal.WithLabelValues("limited")); int(got) != limited {
		t.Errorf("limited counter %v, want %d", got, limited)
	}
	if g.get(g.nearby, "203.0.113.21").Code != 200 {
		t.Error("another visitor was limited")
	}
	if g.hits.Load() != 0 {
		t.Error("upstream reached")
	}
}

// The install-wide budget binds across visitors: no single client is near its own budget, and the first 429 comes after the global budget is
// spent (a token bucket refills a little while the loop runs, so the count is a floor, not exact).
func TestDemoRadarGlobalLimit(t *testing.T) {
	g := newDemoRadarRig(t)
	ok := 0
	for i := 0; i < demoRadarGlobalPerMin+1000; i++ {
		client := "198.51.100." + strconv.Itoa(i%200+1)
		if g.get(g.nearby, client).Code == http.StatusTooManyRequests {
			break
		}
		ok++
	}
	if ok < demoRadarGlobalPerMin || ok >= demoRadarGlobalPerMin+1000 {
		t.Fatalf("%d requests answered before the first 429, want at least %d and a 429 within the next 1000", ok, demoRadarGlobalPerMin)
	}
	if got := testutil.ToFloat64(g.m.DemoRadarTotal.WithLabelValues("limited")); got < 1 {
		t.Errorf("limited counter %v", got)
	}
}

// The signed-in radar stays out of the demo's reach, and the demo routes are in the standalone demo's allowlist.
func TestDemoRadarAllowlist(t *testing.T) {
	for _, p := range []string{"/api/demo/radar/frame", "/api/demo/radar/5/5/12.png"} {
		if !demoOnlyPublic(http.MethodGet, p, "rv-test-1") {
			t.Errorf("standalone demo refuses %s", p)
		}
		if demoOnlyPublic(http.MethodPost, p, "rv-test-1") {
			t.Errorf("standalone demo allows POST %s", p)
		}
	}
	if demoOnlyPublic(http.MethodGet, "/api/maps/radar/frame", "rv-test-1") || demoOnlyPublic(http.MethodGet, "/api/demo/radar/x/../../maps/radar/frame", "rv-test-1") {
		t.Error("standalone demo reaches the signed-in radar")
	}
}

func TestConfigAdvertisesDemoRadar(t *testing.T) {
	if got := configTiles(t, configExtras{Radar: true}); got["radar_demo_url"] != nil {
		t.Errorf("demo radar advertised unmounted: %v", got)
	}
	got := configTiles(t, configExtras{DemoRadar: true})
	if got["radar_demo_url"] != "/api/demo/radar/{z}/{x}/{y}.png" || got["radar_demo_frame_url"] != "/api/demo/radar/frame" ||
		got["radar_demo_minzoom"] != float64(4) || got["radar_demo_maxzoom"] != float64(7) || got["radar_url"] != nil {
		t.Errorf("%v", got)
	}
}
