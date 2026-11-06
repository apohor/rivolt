package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/metrics"
)

// whatIfFake routes with a fixed road factor and speed so the numbers are checkable by hand: length = great circle x 1.2, 20 m/s.
type whatIfFake struct {
	mu      sync.Mutex
	err     error
	callLen []int
}

func (f *whatIfFake) Enabled() bool { return true }
func (f *whatIfFake) RouteChecked(_ context.Context, locs [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	f.mu.Lock()
	f.callLen = append(f.callLen, len(locs))
	f.mu.Unlock()
	if f.err != nil {
		return maps.Route{}, f.err
	}
	var rt maps.Route
	for i := 0; i+1 < len(locs); i++ {
		d := distM(locs[i][0], locs[i][1], locs[i+1][0], locs[i+1][1]) * 1.2
		rt.Legs = append(rt.Legs, maps.RouteLeg{LengthMeters: d, TimeSec: d / 20, Shape: [][2]float64{locs[i], locs[i+1]}})
	}
	return rt, nil
}

func f64(v float64) *float64 { return &v }

// A plan along a meridian: origin (0,0 m), a via, a planner charger at 400 km, a via, the destination at 640 km.
func whatIfPlan() []whatIfStop {
	return []whatIfStop{
		{Lat: 34.0, Lon: -118.0, Source: "origin", DepartureAt: "2026-11-14T16:00:00Z", DepartureSoC: f64(80), DistanceFromOriginM: 0},
		{Lat: 35.0, Lon: -118.0, Source: "via", ArrivalSoC: f64(66.1), DepartureSoC: f64(66.1), DistanceFromOriginM: 111000},
		{Lat: 37.6, Lon: -118.0, Source: "planner", ArrivalAt: "2026-11-14T19:00:00Z", ArrivalSoC: f64(30), DepartureAt: "2026-11-14T19:30:00Z", DepartureSoC: f64(80), DistanceFromOriginM: 400000},
		{Lat: 38.5, Lon: -118.0, Source: "via", ArrivalSoC: f64(67.5), DepartureSoC: f64(67.5), DistanceFromOriginM: 500000},
		{Lat: 39.8, Lon: -118.0, Source: "destination", ArrivalAt: "2026-11-14T22:02:00Z", ArrivalSoC: f64(35), DistanceFromOriginM: 640000},
	}
}

func runWhatIf(t *testing.T, router geometryRouter, m *metrics.Metrics, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	handleTripWhatIf(router, m)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/what-if", bytes.NewReader(b)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func addEdit(index int, lat, lon float64) map[string]any {
	return map[string]any{"kind": "add", "index": index, "via": map[string]any{"lat": lat, "lon": lon}}
}

func TestWhatIfAddRoutesBothSidesAndLeadsWithTheDelta(t *testing.T) {
	r := &whatIfFake{}
	// add a via between the charger (2) and the next via (3), 0.2 degrees east of the road
	code, out := runWhatIf(t, r, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(3, 38.0, -117.8)})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if len(r.callLen) != 2 {
		t.Fatalf("%d Valhalla requests, want 2 (the old stretch and the new one)", len(r.callLen))
	}
	seen := map[int]bool{r.callLen[0]: true, r.callLen[1]: true}
	if !seen[2] || !seen[3] {
		t.Errorf("the two sides should be (P,N) with 2 points and (P,X,N) with 3: %v", r.callLen)
	}
	if out["estimate"] != true || out["estimated"] != false || out["notice"] != "estimate_replan" {
		t.Errorf("flags: %v", out)
	}
	seg := out["segment"].(map[string]any)
	if seg["from"] != float64(2) || seg["to"] != float64(3) {
		t.Errorf("segment = %v, want 2 to 3", seg)
	}
	if out["added_m"].(float64) <= 0 || out["added_sec"].(float64) <= 0 || out["arrival_shift_sec"] != out["added_sec"] {
		t.Errorf("a detour must add distance and time, and the shift equals the added time: %v", out)
	}
	// both sides from the same router: the added time is exactly added distance / 20 m/s (to the second)
	if d := out["added_m"].(float64)/20 - out["added_sec"].(float64); d > 1 || d < -1 {
		t.Errorf("added_m %v and added_sec %v are not from one router", out["added_m"], out["added_sec"])
	}
	line := maps.DecodePolyline6(out["geometry"].(string))
	if out["geometry_precision"] != float64(6) || len(line) < 3 || line[0] != [2]float64{37.6, -118.0} || line[len(line)-1] != [2]float64{38.5, -118.0} {
		t.Errorf("geometry %v (precision %v)", line, out["geometry_precision"])
	}
}

func TestWhatIfClockTimesAreSecondaryRoundedAndOnlyWithTheNeededTimes(t *testing.T) {
	_, out := runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(3, 38.0, -117.8)})
	dest := out["destination"].(map[string]any)["arrival_at"].(string)
	atEdit := out["at_edit"].(map[string]any)["arrival_at"].(string)
	for _, s := range []string{dest, atEdit} {
		if !strings.HasSuffix(s, "0:00Z") && !strings.HasSuffix(s, "5:00Z") {
			t.Errorf("%q is not rounded to 5 minutes", s)
		}
	}
	if dest < "2026-11-14T22:02:00Z" {
		t.Errorf("a detour cannot bring the destination earlier: %s", dest)
	}
	// no times in the plan: no clock lines, and the delta is still there
	stops := whatIfPlan()
	for i := range stops {
		stops[i].ArrivalAt, stops[i].DepartureAt = "", ""
	}
	_, out = runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": stops, "edit": addEdit(3, 38.0, -117.8)})
	if out["at_edit"] != nil || out["destination"] != nil || out["added_sec"] == nil {
		t.Errorf("without times: %v", out)
	}
}

func TestWhatIfBatteryIsAChangeWithOneWayWarnings(t *testing.T) {
	// a modest detour: a drop at the next charging stop (index 2), no warning
	_, out := runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(2, 36.0, -117.9)})
	nc, _ := out["next_charge"].(map[string]any)
	if nc == nil || nc["index"] != float64(2) || nc["is_destination"] != false || nc["soc_drop_pts"].(float64) <= 0 {
		t.Fatalf("next_charge = %v", out["next_charge"])
	}
	if out["warnings"] != nil {
		t.Errorf("a small drop should not warn: %v", out["warnings"])
	}
	if strings.Contains(string(mustJSON(out)), "arrival_soc") {
		t.Error("an absolute SoC must never be returned")
	}
	// a huge detour: the estimate falls below the reserve, the warning names the charger
	_, out = runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(2, 36.0, -112.0)})
	if w, _ := out["warnings"].([]any); len(w) != 1 || w[0] != "may_not_reach_next_charge" {
		t.Errorf("warnings = %v", out["warnings"])
	}
	// an edit after the last charger is judged against the destination
	_, out = runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(4, 39.0, -112.0)})
	nc, _ = out["next_charge"].(map[string]any)
	if nc == nil || nc["is_destination"] != true || nc["index"] != float64(4) {
		t.Fatalf("after the last charger: %v", out["next_charge"])
	}
	if w, _ := out["warnings"].([]any); len(w) != 1 || w[0] != "may_not_reach_destination" {
		t.Errorf("warnings = %v", out["warnings"])
	}
}

// Nothing is claimed when the plan lacks a number, and a shortcut never claims a gain.
func TestWhatIfMakesNoClaimWithoutTheNumbersAndNeverClaimsAGain(t *testing.T) {
	stops := whatIfPlan()
	stops[2].ArrivalSoC = nil
	stops[2].DepartureSoC = nil
	_, out := runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": stops, "edit": addEdit(2, 36.0, -112.0)})
	if out["next_charge"] != nil || out["warnings"] != nil {
		t.Errorf("a missing SoC must mean no claim: %v %v", out["next_charge"], out["warnings"])
	}
	// removing a via that was a long detour shortens the trip: time/distance say so, the battery says nothing
	stops = whatIfPlan()
	stops[1].Lat, stops[1].Lon = 36.0, -112.0
	_, out = runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": stops, "edit": map[string]any{"kind": "remove", "index": 1}})
	if out["added_m"].(float64) >= 0 {
		t.Fatalf("removing a far via should shorten the trip: %v", out["added_m"])
	}
	if out["next_charge"] != nil || out["warnings"] != nil {
		t.Errorf("a shortcut must not claim a gain: %v", out)
	}
	if out["at_edit"] != nil {
		t.Errorf("a removed via has no arrival: %v", out["at_edit"])
	}
}

func TestWhatIfMoveAndRemoveOnlyForVias(t *testing.T) {
	r := &whatIfFake{}
	code, out := runWhatIf(t, r, nil, map[string]any{"stops": whatIfPlan(), "edit": map[string]any{"kind": "move", "index": 1, "via": map[string]any{"lat": 35.0, "lon": -117.0}}})
	if code != 200 || out["segment"].(map[string]any)["from"] != float64(0) || out["segment"].(map[string]any)["to"] != float64(2) {
		t.Errorf("move: %d %v", code, out)
	}
	for _, e := range []map[string]any{
		{"kind": "move", "index": 2, "via": map[string]any{"lat": 35.0, "lon": -117.0}}, // a planner charger is not an edit
		{"kind": "remove", "index": 0},
		{"kind": "remove", "index": 4},
		{"kind": "remove", "index": 9},
	} {
		if code, _ := runWhatIf(t, r, nil, map[string]any{"stops": whatIfPlan(), "edit": e}); code != 400 {
			t.Errorf("%v: %d, want 400", e, code)
		}
	}
}

func TestWhatIfRefusesBadInput(t *testing.T) {
	r := &whatIfFake{}
	many := make([]whatIfStop, 61)
	for i := range many {
		many[i] = whatIfStop{Lat: 34, Lon: -118, Source: "via"}
	}
	bad := whatIfPlan()
	bad[1].Lat = 123
	badSoC := whatIfPlan()
	badSoC[2].ArrivalSoC = f64(140)
	for name, body := range map[string]any{
		"61 stops":         map[string]any{"stops": many, "edit": addEdit(1, 34, -118)},
		"one stop":         map[string]any{"stops": whatIfPlan()[:1], "edit": addEdit(1, 34, -118)},
		"a bad stop":       map[string]any{"stops": bad, "edit": addEdit(1, 34, -118)},
		"a bad via":        map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34, 999)},
		"no via on an add": map[string]any{"stops": whatIfPlan(), "edit": map[string]any{"kind": "add", "index": 1}},
		"a bad SoC":        map[string]any{"stops": badSoC, "edit": addEdit(1, 34, -118)},
		"an unknown kind":  map[string]any{"stops": whatIfPlan(), "edit": map[string]any{"kind": "swap", "index": 1}},
		"add at index 0":   map[string]any{"stops": whatIfPlan(), "edit": addEdit(0, 34, -118)},
		"add past the end": map[string]any{"stops": whatIfPlan(), "edit": addEdit(5, 34, -118)},
	} {
		if code, _ := runWhatIf(t, r, nil, body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	// a body over 64 KiB
	rec := httptest.NewRecorder()
	handleTripWhatIf(r, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/what-if", strings.NewReader(`{"stops":[],"pad":"`+strings.Repeat("a", whatIfBodyMax)+`"}`)))
	if rec.Code != 400 {
		t.Errorf("a body over the cap: %d", rec.Code)
	}
	if len(r.callLen) != 0 {
		t.Errorf("a refused request reached Valhalla %d times", len(r.callLen))
	}
}

// Valhalla out, off, or refusing a shape: both sides fall back to the same rough estimate, flagged, with no line; a refusal is counted.
func TestWhatIfFallsBackToAFlaggedEstimate(t *testing.T) {
	m := metrics.New()
	for name, router := range map[string]geometryRouter{
		"no router": nil,
		"down":      &whatIfFake{err: errors.New("valhalla: HTTP 503")},
		"refused":   &whatIfFake{err: &maps.BadShapeError{Kind: "detour", Reason: "strays"}},
	} {
		code, out := runWhatIf(t, router, m, map[string]any{"stops": whatIfPlan(), "edit": addEdit(3, 38.0, -117.8)})
		if code != 200 || out["estimated"] != true || out["geometry"] != nil || out["added_m"].(float64) <= 0 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if got := testutil.ToFloat64(m.RoutePointRefusedTotal.WithLabelValues("plan_detour")); got < 1 {
		t.Errorf("a refused shape should be counted: %v", got)
	}
}
