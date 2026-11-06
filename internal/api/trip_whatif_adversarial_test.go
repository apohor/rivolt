package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/maps"
)

// Adversarial cases for POST /api/trips/what-if, written by someone other than the author (the cross-review, as for Pro 29 and 30). Test
// data only: made-up coordinates, a fake router, no Rivian, no account. A case that fails is a finding, listed for DA; the numbers refer to
// _shared/what-if-adversarial-cases.md.

func advPost(h http.HandlerFunc, raw string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/trips/what-if", strings.NewReader(raw)))
	return rec
}

// 1: bodies that are not a request at all are refused, never a 500 and never a router call.
func TestWhatIfAdvBodiesThatAreNotARequest(t *testing.T) {
	r := &whatIfFake{}
	h := handleTripWhatIf(r, nil)
	for name, raw := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "string": `"x"`, "number": "7", "truncated": `{"stops":[{"lat":1`,
		"no stops": `{"edit":{"kind":"add","index":1}}`, "stops not an array": `{"stops":5,"edit":{"kind":"remove","index":1}}`,
		"coordinate as a string": `{"stops":[{"lat":"1","lon":2},{"lat":3,"lon":4}],"edit":{"kind":"add","index":1,"via":{"lat":1,"lon":1}}}`,
		"index as a float":       `{"stops":[{"lat":1,"lon":2},{"lat":3,"lon":4}],"edit":{"kind":"add","index":1.5,"via":{"lat":1,"lon":1}}}`,
		"index too big for int":  `{"stops":[{"lat":1,"lon":2},{"lat":3,"lon":4}],"edit":{"kind":"add","index":99999999999999999999,"via":{"lat":1,"lon":1}}}`,
	} {
		if rec := advPost(h, raw); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if len(r.callLen) != 0 {
		t.Errorf("a refused request reached the router %d times", len(r.callLen))
	}
}

// 4: coordinates at the edges. (0,0) is a place and is accepted; 1e308 and out-of-range values are refused; the error never echoes them.
func TestWhatIfAdvCoordinateEdgesAndNoEcho(t *testing.T) {
	r := &whatIfFake{}
	h := handleTripWhatIf(r, nil)
	body := func(lat, lon string) string {
		return `{"stops":[{"lat":` + lat + `,"lon":` + lon + `},{"lat":10,"lon":10.5}],"edit":{"kind":"add","index":1,"via":{"lat":10,"lon":10.2}}}`
	}
	for _, c := range [][2]string{{"90.0001", "0"}, {"0", "180.0001"}, {"-90.5", "0"}, {"1e308", "0"}, {"0", "-1e308"}} {
		rec := advPost(h, body(c[0], c[1]))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", c, rec.Code)
		}
		if strings.Contains(rec.Body.String(), strings.TrimLeft(c[0], "-")) && c[0] != "0" {
			t.Errorf("the error echoes the coordinate: %q", rec.Body.String())
		}
	}
	if rec := advPost(h, body("0", "0")); rec.Code != http.StatusOK {
		t.Errorf("(0,0) is a valid place: %d", rec.Code)
	}
	if rec := advPost(h, body("-0", "0")); rec.Code != http.StatusOK {
		t.Errorf("-0 latitude: %d", rec.Code)
	}
}

// 3 / 31: an oversized body is cut at the cap: the handler never reads far past 64 KiB, and answers 400.
type advCountingReader struct{ n int }

func (c *advCountingReader) Read(p []byte) (int, error) {
	if c.n == 0 {
		c.n += copy(p, `{"stops":[`)
		return c.n, nil
	}
	for i := range p {
		p[i] = ' '
	}
	c.n += len(p)
	return len(p), nil // an endless stream of spaces
}

func TestWhatIfAdvAnEndlessBodyIsCutAtTheCap(t *testing.T) {
	cr := &advCountingReader{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trips/what-if", io.NopCloser(cr))
	req.ContentLength = 100 // a lying Content-Length
	handleTripWhatIf(&whatIfFake{}, nil)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	if cr.n > whatIfBodyMax+16<<10 {
		t.Errorf("read %d bytes of an endless body, the cap is %d", cr.n, whatIfBodyMax)
	}
}

// 1 (trailing garbage) and 10 (unknown fields): pinned as they are, so a change is deliberate.
func TestWhatIfAdvTrailingGarbageAndUnknownFieldsArePinned(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	good, _ := json.Marshal(map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34.5, -118), "surprise": 1})
	if rec := advPost(h, string(good)); rec.Code != http.StatusOK {
		t.Errorf("an unknown field: %d", rec.Code)
	}
	if rec := advPost(h, string(good)+` {"x":1}`); rec.Code != http.StatusOK {
		t.Logf("FINDING (low): a second JSON value after the request is now refused (%d)", rec.Code)
	} else {
		t.Log("FINDING (low): trailing data after the JSON request is ignored, not refused")
	}
}

// 5: source values. Only vias are edited, so only `via` matters; pin what an unknown source does.
func TestWhatIfAdvSourceValues(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	plan := whatIfPlan()
	plan[1].Source = "VIA" // wrong case: not a via, so not editable
	b, _ := json.Marshal(map[string]any{"stops": plan, "edit": map[string]any{"kind": "remove", "index": 1}})
	if rec := advPost(h, string(b)); rec.Code != http.StatusBadRequest {
		t.Errorf("a via spelled VIA was edited: %d", rec.Code)
	}
	plan = whatIfPlan()
	plan[0].Source, plan[4].Source = "destination", "origin" // swapped ends
	b, _ = json.Marshal(map[string]any{"stops": plan, "edit": addEdit(1, 34.5, -118)})
	if rec := advPost(h, string(b)); rec.Code == http.StatusOK {
		t.Log("FINDING (low): the first and last stops' sources are not checked; swapped origin and destination are answered")
	}
}

// 6: edit.index edges for move and remove: the first stop, the last, and out of range, all refused.
func TestWhatIfAdvEditIndexEdges(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	for _, idx := range []int{-1, 0, 4, 5, math.MaxInt32} {
		for _, kind := range []string{"move", "remove"} {
			b, _ := json.Marshal(map[string]any{"stops": whatIfPlan(), "edit": map[string]any{"kind": kind, "index": idx, "via": map[string]any{"lat": 34.2, "lon": -118}}})
			if rec := advPost(h, string(b)); rec.Code != http.StatusBadRequest {
				t.Errorf("%s at %d: %d, want 400", kind, idx, rec.Code)
			}
		}
	}
	// remove of the only via (index 1 of 3 stops) is allowed and is a shortcut or equal, never an error
	three := whatIfPlan()[:3]
	three[1].Source = "via"
	three[2].Source = "destination"
	b, _ := json.Marshal(map[string]any{"stops": three, "edit": map[string]any{"kind": "remove", "index": 1}})
	if rec := advPost(h, string(b)); rec.Code != http.StatusOK {
		t.Errorf("remove of the only via: %d %q", rec.Code, rec.Body.String())
	}
}

// 8: absurd times and SoC. Nothing may turn into a 5xx or a non-JSON body.
func TestWhatIfAdvAbsurdTimesAndNumbersDoNotBreakTheAnswer(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	for name, mut := range map[string]func(p []whatIfStop){
		"year 9999 arrival":    func(p []whatIfStop) { p[4].ArrivalAt = "9999-12-31T23:59:59Z" },
		"year 0001 departure":  func(p []whatIfStop) { p[0].DepartureAt = "0001-01-01T00:00:00Z" },
		"offset zone":          func(p []whatIfStop) { p[0].DepartureAt = "2026-11-14T16:00:00+14:00" },
		"malformed time":       func(p []whatIfStop) { p[0].DepartureAt = "tomorrow-ish" },
		"huge distance":        func(p []whatIfStop) { p[4].DistanceFromOriginM = 1e300 },
		"negative distance":    func(p []whatIfStop) { p[2].DistanceFromOriginM = -5e6 },
		"decreasing distances": func(p []whatIfStop) { p[3].DistanceFromOriginM = 10 },
		"arrival below depart": func(p []whatIfStop) { p[2].ArrivalSoC, p[2].DepartureSoC = f64(10), f64(0) },
	} {
		plan := whatIfPlan()
		mut(plan)
		b, _ := json.Marshal(map[string]any{"stops": plan, "edit": addEdit(3, 38.0, -117.0)})
		rec := advPost(h, string(b))
		var out map[string]any
		if rec.Code >= 500 || json.Unmarshal(rec.Body.Bytes(), &out) != nil && rec.Code == http.StatusOK {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
			continue
		}
		for k, v := range out {
			if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
				t.Errorf("%s: %s is %v", name, k, f)
			}
		}
		if nc, ok := out["next_charge"].(map[string]any); ok {
			if d, _ := nc["soc_drop_pts"].(float64); d < 0 || d > 100000 {
				t.Errorf("%s: an absurd battery change %v", name, d)
			}
		}
	}
}

// 9: target_arrival_soc_percent is not validated here (the planner endpoint refuses it outside 0 to 100). Pin it.
func TestWhatIfAdvTargetArrivalSoCIsUnvalidated(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	for _, v := range []float64{-50, 500} {
		b, _ := json.Marshal(map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 36.5, -119.5), "target_arrival_soc_percent": v})
		if rec := advPost(h, string(b)); rec.Code == http.StatusOK {
			t.Logf("FINDING (low): target_arrival_soc_percent %v is accepted; the plan endpoint refuses it outside 0 to 100", v)
		}
	}
}

// 29: the same-source delta. A via on the P to N line costs about nothing; its removal is the mirror; an off-line add and the remove of
// that via are equal and opposite. Proves Valhalla is used on both sides.
func TestWhatIfAdvSameSourceDelta(t *testing.T) {
	h := handleTripWhatIf(&whatIfFake{}, nil)
	two := func(mid *[2]float64) []map[string]any {
		s := []map[string]any{{"lat": 34.0, "lon": -118.0, "source": "origin"}}
		if mid != nil {
			s = append(s, map[string]any{"lat": mid[0], "lon": mid[1], "source": "via"})
		}
		return append(s, map[string]any{"lat": 35.0, "lon": -118.0, "source": "destination"})
	}
	call := func(stops []map[string]any, edit map[string]any) map[string]any {
		b, _ := json.Marshal(map[string]any{"stops": stops, "edit": edit})
		rec := advPost(h, string(b))
		var out map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
		return out
	}
	on := &[2]float64{34.5, -118.0}
	add := call(two(nil), map[string]any{"kind": "add", "index": 1, "via": map[string]any{"lat": 34.5, "lon": -118.0}})
	if m := add["added_m"].(float64); math.Abs(m) > 1 {
		t.Errorf("a via on the line added %v m", m)
	}
	rem := call(two(on), map[string]any{"kind": "remove", "index": 1})
	if m := rem["added_m"].(float64); math.Abs(m) > 1 {
		t.Errorf("removing a via on the line changed %v m", m)
	}
	off := &[2]float64{34.5, -117.0}
	a := call(two(nil), map[string]any{"kind": "add", "index": 1, "via": map[string]any{"lat": off[0], "lon": off[1]}})
	r := call(two(off), map[string]any{"kind": "remove", "index": 1})
	if math.Abs(a["added_m"].(float64)+r["added_m"].(float64)) > 0.2 || math.Abs(a["added_sec"].(float64)+r["added_sec"].(float64)) > 1 {
		t.Errorf("add %v / %v and remove %v / %v are not equal and opposite", a["added_m"], a["added_sec"], r["added_m"], r["added_sec"])
	}
	if a["added_m"].(float64) <= 0 || r["added_m"].(float64) >= 0 {
		t.Errorf("signs: add %v, remove %v", a["added_m"], r["added_m"])
	}
}

// 30: every clock time in the answer is on a 5-minute mark, and none stands without a delta.
func TestWhatIfAdvClockTimesAreOnFiveMinuteMarks(t *testing.T) {
	code, out := runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(3, 38.2, -117.5)})
	if code != 200 {
		t.Fatal(code)
	}
	if _, ok := out["added_sec"]; !ok {
		t.Fatal("no delta in the answer")
	}
	for _, key := range []string{"at_edit", "destination"} {
		m, ok := out[key].(map[string]any)
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, m["arrival_at"].(string))
		if err != nil || at.Minute()%5 != 0 || at.Second() != 0 {
			t.Errorf("%s arrival %v is not on a 5-minute mark (%v)", key, m["arrival_at"], err)
		}
	}
}

// 32: with the plan's SoC numbers absent there are no warnings and no battery fields at all, however long the detour.
func TestWhatIfAdvNoBatteryClaimWithoutSoC(t *testing.T) {
	plan := whatIfPlan()
	for i := range plan {
		plan[i].ArrivalSoC, plan[i].DepartureSoC = nil, nil
	}
	code, out := runWhatIf(t, &whatIfFake{}, nil, map[string]any{"stops": plan, "edit": addEdit(3, 45, -100)}) // a very long detour
	if code != 200 {
		t.Fatal(code)
	}
	for _, k := range []string{"warnings", "next_charge"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s present with no SoC numbers", k)
		}
	}
	s := string(advJSON(out))
	for _, bad := range []string{"soc", "SoC"} {
		if strings.Contains(s, bad) {
			t.Errorf("the answer mentions %q with no SoC numbers: %s", bad, s)
		}
	}
}

func advJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// 15: a router that never returns but honours its context: the handler answers (estimated) when the request context ends, and does not leak.
type advBlockingRouter struct{}

func (advBlockingRouter) Enabled() bool { return true }
func (advBlockingRouter) RouteChecked(ctx context.Context, _ [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	<-ctx.Done()
	return maps.Route{}, ctx.Err()
}

func TestWhatIfAdvAHangingRouterIsCutByTheContext(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34.5, -118)})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/trips/what-if", bytes.NewReader(b)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handleTripWhatIf(advBlockingRouter{}, nil)(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler did not return after its context ended")
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["estimated"] != true || out["geometry"] != nil {
		t.Errorf("%d %v", rec.Code, out)
	}
}

// 16: "no route", an empty shape, a one-point shape and a NaN shape all end in a flagged estimate or a line-less answer, never a 5xx.
type advShapeRouter struct {
	shape [][2]float64
	err   error
}

func (advShapeRouter) Enabled() bool { return true }
func (s advShapeRouter) RouteChecked(_ context.Context, locs [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	if s.err != nil {
		return maps.Route{}, s.err
	}
	var rt maps.Route
	for i := 0; i+1 < len(locs); i++ {
		rt.Legs = append(rt.Legs, maps.RouteLeg{LengthMeters: 1000, TimeSec: 60, Shape: s.shape})
	}
	return rt, nil
}

func TestWhatIfAdvRouterOddities(t *testing.T) {
	for name, r := range map[string]advShapeRouter{
		"no route": {err: maps.ErrNoRoute}, "empty shape": {shape: nil}, "one point": {shape: [][2]float64{{34, -118}}},
		"nan shape": {shape: [][2]float64{{math.NaN(), 0}, {1, 1}}},
	} {
		code, out := runWhatIf(t, r, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34.5, -118)})
		if code != http.StatusOK {
			t.Errorf("%s: %d", name, code)
			continue
		}
		if g, _ := out["geometry"].(string); g != "" {
			for _, p := range maps.DecodePolyline6(g) {
				if math.IsNaN(p[0]) || math.Abs(p[0]) > 90 {
					t.Errorf("%s: a bad point in the served line", name)
				}
			}
		}
	}
}

// 16 / 27: a very long edited stretch (a far via on a long route) has a bounded line. FINDING if not: #1569 thins and caps the plan's lines, but
// this answer encodes the raw shape.
func TestWhatIfAdvTheServedLineIsBounded(t *testing.T) {
	n := 120_000
	shape := make([][2]float64, n)
	for i := range shape {
		f := float64(i) / float64(n-1)
		shape[i] = [2]float64{34 + 5*f, -118 + 0.01*math.Sin(f*500)}
	}
	code, out := runWhatIf(t, advShapeRouter{shape: shape}, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34.5, -118)})
	if code != http.StatusOK {
		t.Fatal(code)
	}
	g, _ := out["geometry"].(string)
	pts := maps.DecodePolyline6(g)
	t.Logf("served %d points / %d bytes for a %d-point shape", len(pts), len(g), 2*n)
	if len(pts) > advGeometryCap {
		t.Errorf("FINDING: the what-if line has %d points (%d bytes), the plan's cap is %d", len(pts), len(g), advGeometryCap)
	}
}

// 28 / 25: a router error never reaches the client as text.
func TestWhatIfAdvErrorsAreNotEchoed(t *testing.T) {
	r := advShapeRouter{err: errors.New("valhalla at 10.1.2.3 said: secret-token-xyz")}
	_, out := runWhatIf(t, r, nil, map[string]any{"stops": whatIfPlan(), "edit": addEdit(1, 34.5, -118)})
	if s := string(advJSON(out)); strings.Contains(s, "secret-token") || strings.Contains(s, "10.1.2.3") {
		t.Errorf("a router error leaked into the answer: %s", s)
	}
}

// advGeometryCap is the plan geometry's per-route cap from #1569 (4,000 points); the same cap is the yardstick for this answer's line.
const advGeometryCap = 4000
