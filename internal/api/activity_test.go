package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/analytics"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/drives"
)

// The cases of web/src/lib/drives.test.ts, against the Go port.
var (
	ptA     = [2]float64{37.7749, -122.4194}
	ptB     = [2]float64{37.8044, -122.2712}
	ptX     = [2]float64{37.3382, -121.8863}
	ptDrift = [2]float64{37.7749 + 0.0036, -122.4194} // A's recorded start ~400 m off
)

var rtSeq int

func rtd(start, end string, from, to [2]float64) rtDrive {
	rtSeq++
	st, _ := time.Parse(time.RFC3339, start)
	en, _ := time.Parse(time.RFC3339, end)
	return rtDrive{d: drives.Drive{
		ID: "d" + string(rune('0'+rtSeq)), VehicleID: "v1", StartedAt: st, EndedAt: en,
		StartSoCPct: 80, EndSoCPct: 75, DistanceMi: 5, MaxSpeedMph: 60, AvgSpeedMph: 30, EnergyUsedKWh: 2,
		StartLat: from[0], StartLon: from[1], EndLat: to[0], EndLon: to[1],
	}}
}

func TestCollapseRoundTripsLikeWeb(t *testing.T) {
	const radius, gap = 200.0, 90 * time.Minute
	t.Run("anchors on the previous drive's end when the outbound start drifted", func(t *testing.T) {
		prev := rtd("2026-07-16T08:00:00Z", "2026-07-16T08:30:00Z", ptX, ptA)
		leg1 := rtd("2026-07-16T09:00:00Z", "2026-07-16T09:20:00Z", ptDrift, ptB)
		leg2 := rtd("2026-07-16T09:40:00Z", "2026-07-16T10:00:00Z", ptB, ptA)
		out := collapseRoundTripsLikeWeb([]rtDrive{leg2, leg1, prev}, radius, gap)
		if len(out) != 2 || out[0].d.ID != leg1.d.ID || out[1].d.ID != prev.d.ID {
			t.Fatalf("out = %+v", out)
		}
		m := out[0]
		if math.Abs(m.d.EndLat-ptA[0]) > 1e-4 || math.Abs(m.d.DistanceMi-10) > 1e-5 || m.legs != 2 || out[1].legs != 0 {
			t.Errorf("merged = %+v", m)
		}
		if m.drivingS != 2400 { // two 20-minute legs, the 20-minute park excluded
			t.Errorf("driving seconds = %v, want 2400", m.drivingS)
		}
	})
	t.Run("merges a clean round trip with no predecessor", func(t *testing.T) {
		leg1 := rtd("2026-07-16T09:00:00Z", "2026-07-16T09:20:00Z", ptA, ptB)
		leg2 := rtd("2026-07-16T09:40:00Z", "2026-07-16T10:00:00Z", ptB, ptA)
		if out := collapseRoundTripsLikeWeb([]rtDrive{leg2, leg1}, radius, gap); len(out) != 1 || math.Abs(out[0].d.DistanceMi-10) > 1e-5 {
			t.Errorf("out = %+v", out)
		}
	})
	t.Run("does not merge a one-way trip", func(t *testing.T) {
		leg1 := rtd("2026-07-16T09:00:00Z", "2026-07-16T09:20:00Z", ptA, ptB)
		leg2 := rtd("2026-07-16T09:40:00Z", "2026-07-16T10:00:00Z", ptB, ptX)
		if out := collapseRoundTripsLikeWeb([]rtDrive{leg2, leg1}, radius, gap); len(out) != 2 {
			t.Errorf("len = %d", len(out))
		}
	})
	t.Run("does not merge across a long park", func(t *testing.T) {
		leg1 := rtd("2026-07-16T09:00:00Z", "2026-07-16T09:20:00Z", ptA, ptB)
		leg2 := rtd("2026-07-16T12:20:00Z", "2026-07-16T12:40:00Z", ptB, ptA)
		if out := collapseRoundTripsLikeWeb([]rtDrive{leg2, leg1}, radius, gap); len(out) != 2 {
			t.Errorf("len = %d", len(out))
		}
	})
}

// mergeChain's energy and cost rules (lib/drives.ts): a leg of real distance whose
// charge dropped but has no energy gates the total to zero (and no cost); a leg
// under 0.1 mi counts for neither; the merged rate is cost over energy.
func TestMergeChainEnergyAndCost(t *testing.T) {
	a := rtd("2026-07-16T09:00:00Z", "2026-07-16T09:20:00Z", ptA, ptB)
	b := rtd("2026-07-16T09:40:00Z", "2026-07-16T10:00:00Z", ptB, ptA)
	a.cost, a.cur = floatPtr(1.0), "USD"
	b.cost = floatPtr(0.5)
	m := mergeChainLikeWeb([]rtDrive{a, b})
	if m.d.EnergyUsedKWh != 4 || m.cost == nil || *m.cost != 1.5 || m.cur != "USD" || m.rate == nil || *m.rate != 0.375 {
		t.Errorf("merged = energy %v cost %v cur %q rate %v", m.d.EnergyUsedKWh, m.cost, m.cur, m.rate)
	}
	// A real leg with a dropped charge and no energy: energy 0, no cost, no rate.
	c := b
	c.d.EnergyUsedKWh, c.cost = 0, nil
	m = mergeChainLikeWeb([]rtDrive{a, c})
	if m.d.EnergyUsedKWh != 0 || m.cost != nil || m.rate != nil {
		t.Errorf("gated merge = energy %v cost %v rate %v", m.d.EnergyUsedKWh, m.cost, m.rate)
	}
	// A tiny stub (0.05 mi, charge dropped, no energy) gates nothing.
	stub := c
	stub.d.DistanceMi = 0.05
	m = mergeChainLikeWeb([]rtDrive{a, stub})
	if m.d.EnergyUsedKWh != 2 || m.cost == nil || *m.cost != 1.0 {
		t.Errorf("stub merge = energy %v cost %v", m.d.EnergyUsedKWh, m.cost)
	}
}

// kind and place with the cluster fallback resolved in, in the web's chargePlaceLabel order.
func TestResolvedKindPlace(t *testing.T) {
	H, P, F, U := analytics.LabelHome, analytics.LabelPublic, analytics.LabelFast, analytics.LabelUnknown
	for _, c := range []struct {
		kind, place  string
		cluster      analytics.ClusterLabel
		wantK, wantP string
	}{
		{"dcfc", "", H, "dcfc", ""}, {"dcfc", "home", P, "dcfc", "home"},
		{"ac", "home", P, "ac", "home"}, {"ac", "away", H, "ac", "away"},
		{"ac", "", H, "ac", "home"}, {"ac", "unknown", P, "ac", "away"}, {"ac", "", F, "ac", ""}, {"ac", "", U, "ac", ""},
		{"", "", H, "", "home"}, {"", "", P, "", "away"}, {"", "", F, "dcfc", ""}, {"", "", U, "", ""},
	} {
		k, p := resolvedKindPlace(c.kind, c.place, c.cluster)
		if k != c.wantK || p != c.wantP {
			t.Errorf("resolvedKindPlace(%q,%q,%q) = %q,%q want %q,%q", c.kind, c.place, c.cluster, k, p, c.wantK, c.wantP)
		}
		// The label the web would show is the same as charge-split's.
		wantLabel := chargePlaceLabel(c.kind, c.place, c.cluster)
		gotLabel := map[[2]string]string{{"dcfc", ""}: "Fast", {"dcfc", "home"}: "Fast", {"dcfc", "away"}: "Fast"}[[2]string{k, p}]
		if gotLabel == "" {
			switch p {
			case "home":
				gotLabel = "Home"
			case "away":
				gotLabel = "Away"
			}
		}
		if gotLabel != wantLabel {
			t.Errorf("(%q,%q,%q): activity says %q, charge-split says %q", c.kind, c.place, c.cluster, gotLabel, wantLabel)
		}
	}
}

// Through the real router with full URLs: 401 signed out, empty lists signed in
// without stores, and the 400s for the window, vehicle and round-trip parameters.
func TestActivityThroughTheRouter(t *testing.T) {
	r := New(Deps{})
	uid := uuid.New()
	get := func(url string, signedIn bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if signedIn {
			req = req.WithContext(auth.WithUser(req.Context(), uid))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := get("https://rivolt.dev/api/activity", false); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", w.Code)
	}
	w := get("https://rivolt.dev/api/activity?vehicle_id=rv-1&since=2026-10-01T00:00:00Z&until=2026-10-20T00:00:00Z&round_trips=0&round_trip_radius_m=300&round_trip_gap_min=60", true)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"drives":[],"charges":[]}` {
		t.Errorf("signed in: %d %s", w.Code, w.Body.String())
	}
	for name, u := range map[string]string{
		"bad time":    "?since=nope",
		"too wide":    "?since=2024-01-01T00:00:00Z&until=2026-01-01T00:00:00Z",
		"bad vehicle": "?vehicle_id=a%20b",
		"bad radius":  "?round_trip_radius_m=0",
		"huge radius": "?round_trip_radius_m=99999",
		"bad gap":     "?round_trip_gap_min=abc",
		"nan radius":  "?round_trip_radius_m=NaN",
		"inf radius":  "?round_trip_radius_m=Inf",
		"nan gap":     "?round_trip_gap_min=nan",
	} {
		if w := get("https://rivolt.dev/api/activity"+u, true); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
}

// activity, charge-split and chart-points share one per-user ceiling (each reads the
// user's rows, two of them cluster the last 5000 charges): past it the answer is 429
// with a Retry-After, for that user only, and counted across the three routes.
func TestAggregateEndpointsAreRateLimitedPerUser(t *testing.T) {
	r := New(Deps{})
	call := func(uid uuid.UUID, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+path, nil)
		req = req.WithContext(auth.WithUser(req.Context(), uid))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	a, b := uuid.New(), uuid.New()
	paths := []string{"/api/charge-split", "/api/activity", "/api/chart-points"}
	limited := 0
	for i := 0; i < 45; i++ {
		w := call(a, paths[i%3])
		if w.Code == http.StatusTooManyRequests {
			limited++
			if ra := w.Header().Get("Retry-After"); ra == "" || ra == "0" {
				t.Fatalf("429 without a usable Retry-After: %q", ra)
			}
		}
	}
	if limited == 0 {
		t.Error("45 calls across the three routes were never limited")
	}
	// The bucket is shared: the route that was never the one to run dry is limited too.
	for _, p := range paths {
		if w := call(a, p); w.Code != http.StatusTooManyRequests {
			t.Errorf("%s after the shared ceiling: %d, want 429", p, w.Code)
		}
	}
	for _, p := range paths {
		if w := call(b, p); w.Code != http.StatusOK {
			t.Errorf("another user's %s: %d, want 200", p, w.Code)
		}
	}
}
