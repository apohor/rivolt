package tripshare

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
)

// eastLine is a straight road going east from (lat, lon) with a vertex every stepM, n vertices.
func eastLine(lat, lon, stepM float64, n int) [][2]float64 {
	degPerM := 1 / (111320.0 * 0.809) // cos(36 deg)
	out := make([][2]float64, n)
	for i := range out {
		out[i] = [2]float64{lat, lon + float64(i)*stepM*degPerM}
	}
	return out
}

func planJSON(t *testing.T, line [][2]float64, wps []rivian.PlannedWaypoint, elev [][2]int) json.RawMessage {
	t.Helper()
	rt := rivian.TripRoute{
		TotalDriveDistanceMeters: 118400, TotalDriveDurationSec: 5000, TotalChargingDurationSec: 900, ArrivalSoC: 31.5,
		Waypoints: wps, Geometry: maps.EncodePolyline6(line), GeometryPrecision: 6,
	}
	m := map[string]any{"Routes": []rivian.TripRoute{rt}, "destination_tz": "America/Los_Angeles",
		"weather_adjustment": map[string]any{"x": 1}, "costs": []any{map[string]any{"dcfc": 12.5}}}
	if elev != nil {
		m["elevation_profile"] = map[string]any{"points": elev}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func basicWps(line [][2]float64) []rivian.PlannedWaypoint {
	last := line[len(line)-1]
	mid := line[len(line)/2]
	return []rivian.PlannedWaypoint{
		{WaypointType: "origin", Source: "origin", Latitude: line[0][0], Longitude: line[0][1], DepartureSoC: 90, Name: "123 Home Street", EntityID: "e1"},
		{WaypointType: "charger", Source: "planner", Name: "Mid Charger", Latitude: mid[0], Longitude: mid[1], ArrivalSoC: 20, DepartureSoC: 80, ChargeDurationSec: 900, MaxPowerKW: 200, ArrivalTimeUTC: "2026-11-20T18:00:00Z", DepartureTimeUTC: "2026-11-20T18:15:00Z", EntityID: "e2"},
		{WaypointType: "destination", Source: "destination", Latitude: last[0], Longitude: last[1], Name: "9 Far Road", EntityID: "e3"},
	}
}

func minDistTo(snap *Snapshot, lat, lon float64) float64 {
	best := 1e12
	for _, s := range snap.Route.Segments {
		for _, p := range maps.DecodePolyline6(s) {
			if d := DistM(lat, lon, p[0], p[1]); d < best {
				best = d
			}
		}
	}
	return best
}

func build(t *testing.T, line [][2]float64, wps []rivian.PlannedWaypoint, home *Circle, cut float64) *Snapshot {
	t.Helper()
	s, err := Build(Input{Title: "Trip", Plan: planJSON(t, line, wps, nil), PlannedAt: time.Unix(1, 0), Home: home, CutM: cut})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenHashOnly(t *testing.T) {
	tok, h, err := NewToken()
	if err != nil || !ValidToken(tok) || len(tok) != 22 {
		t.Fatalf("token %q err %v", tok, err)
	}
	if string(h) == tok || len(h) != 32 {
		t.Fatal("hash must be a 32-byte digest")
	}
	if string(HashToken(tok)) != string(h) {
		t.Fatal("hash must be reproducible")
	}
	tok2, _, _ := NewToken()
	if tok == tok2 {
		t.Fatal("tokens repeat")
	}
	for _, bad := range []string{"", "short", tok + "x", strings.Repeat("!", 22)} {
		if ValidToken(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestCutKeepsMinDistanceFromEnds(t *testing.T) {
	line := eastLine(36.0, -115.0, 1000, 120)
	wps := basicWps(line)
	for _, cut := range []float64{MinCutM, 4500, MaxCutM} {
		s := build(t, line, wps, nil, cut)
		for _, p := range [][2]float64{line[0], line[len(line)-1]} {
			if d := minDistTo(s, p[0], p[1]); d < 3000 {
				t.Fatalf("cut %v: kept line comes within %.0f m of an end", cut, d)
			}
		}
		if len(s.Route.Segments) != 1 {
			t.Fatalf("segments %d", len(s.Route.Segments))
		}
	}
}

func TestCutAroundViaAndOvernight(t *testing.T) {
	line := eastLine(36.0, -115.0, 1000, 120)
	wps := basicWps(line)
	via := line[40]
	night := line[80]
	wps = append(wps[:2], append([]rivian.PlannedWaypoint{
		{WaypointType: "charger", Source: "via", Name: "Aunt's house", Latitude: via[0], Longitude: via[1], EntityID: "v"},
		{WaypointType: "charger", Source: "overnight", Name: "Hotel X", Latitude: night[0], Longitude: night[1]},
	}, wps[2:]...)...)
	s := build(t, line, wps, nil, 4000)
	for _, p := range [][2]float64{via, night} {
		if d := minDistTo(s, p[0], p[1]); d < 3000 {
			t.Fatalf("kept line within %.0f m of a via/overnight", d)
		}
	}
	if len(s.Route.Segments) != 3 {
		t.Fatalf("want 3 segments, got %d", len(s.Route.Segments))
	}
	for _, st := range s.Stops {
		if st.Name == "Aunt's house" || st.Name == "Hotel X" {
			t.Fatal("private stop name leaked")
		}
	}
}

func TestLoopedRouteIsCutOnReentry(t *testing.T) {
	// Out east 60 km, then back west to near the start, then out again: the road re-enters the start circle.
	out := eastLine(36.0, -115.0, 1000, 60)
	back := eastLine(36.0, -115.0, 1000, 60)
	var line [][2]float64
	line = append(line, out...)
	for i := len(back) - 2; i >= 0; i-- {
		line = append(line, [2]float64{36.0002, back[i][1]})
	}
	line = append(line, eastLine(36.0, -115.0, 1000, 40)[1:]...)
	wps := basicWps(line)
	s := build(t, line, wps, nil, 5000)
	if d := minDistTo(s, line[0][0], line[0][1]); d < 3000 {
		t.Fatalf("looped route comes back within %.0f m of the origin", d)
	}
	if len(s.Route.Segments) < 2 {
		t.Fatalf("a re-entry must split the line, got %d segments", len(s.Route.Segments))
	}
}

func TestHomeWidensCut(t *testing.T) {
	line := eastLine(36.0, -115.0, 1000, 120)
	home := &Circle{Lat: line[0][0], Lon: line[0][1], RadiusM: 4000}
	s := build(t, line, basicWps(line), home, MinCutM)
	if d := minDistTo(s, home.Lat, home.Lon); d < 7000 {
		t.Fatalf("home radius + 3 km not honoured: %.0f", d)
	}
}

func TestShortTripRefused(t *testing.T) {
	line := eastLine(36.0, -115.0, 500, 20) // 9.5 km: 1.0 km is left once 4.5 km is cut at each end
	_, err := Build(Input{Plan: planJSON(t, line, basicWps(line), nil), CutM: 4500})
	if err != ErrTooShort {
		t.Fatalf("got %v", err)
	}
}

func TestSameOriginAndDestinationRefusedOrCut(t *testing.T) {
	out := eastLine(36.0, -115.0, 1000, 50)
	line := append([][2]float64{}, out...)
	for i := len(out) - 2; i >= 0; i-- {
		line = append(line, out[i])
	}
	s, err := Build(Input{Plan: planJSON(t, line, basicWps(line), nil), CutM: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if d := minDistTo(s, line[0][0], line[0][1]); d < 3000 {
		t.Fatalf("within %.0f m", d)
	}
}

func TestUnsupportedPlan(t *testing.T) {
	for _, p := range []string{`{}`, `{"Routes":[]}`, `{"days":[{}]}`, `not json`, `{"Routes":[{"Geometry":""}]}`} {
		if _, err := Build(Input{Plan: json.RawMessage(p), CutM: MinCutM}); err != ErrUnsupported {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestSnapshotForbiddenKeysAndValues(t *testing.T) {
	line := eastLine(36.0123, -115.0371, 1000, 120)
	s := build(t, line, basicWps(line), nil, 4000)
	b, _ := json.Marshal(s)
	var v any
	_ = json.Unmarshal(b, &v)
	forbidden := []string{"vin", "vehicle", "user", "email", "entity", "cost", "advice", "weather", "url", "home", "address", "preference", "adapter", "trailer", "token"}
	var walk func(any)
	walk = func(x any) {
		switch t2 := x.(type) {
		case map[string]any:
			for k, vv := range t2 {
				lk := strings.ToLower(k)
				for _, f := range forbidden {
					if strings.Contains(lk, f) {
						t.Errorf("forbidden key %q", k)
					}
				}
				walk(vv)
			}
		case []any:
			for _, e := range t2 {
				walk(e)
			}
		}
	}
	walk(v)
	for _, bad := range []string{"123 Home Street", "9 Far Road", "e1", "e2", "e3"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("snapshot contains %q", bad)
		}
	}
	if s.Totals.DistanceM%1000 != 0 {
		t.Errorf("distance not rounded to 1 km: %d", s.Totals.DistanceM)
	}
	if s.Start.Lat == line[0][0] {
		t.Error("start not rounded")
	}
	if len(s.Stops) != 1 || s.Stops[0].Kind != "charger" || s.Stops[0].Name != "Mid Charger" || s.Stops[0].AtM == nil {
		t.Errorf("stops: %+v", s.Stops)
	}
}

func TestChargerInsideCutBecomesGenericStop(t *testing.T) {
	line := eastLine(36.0, -115.0, 1000, 120)
	wps := basicWps(line)
	near := line[2]
	wps[1].Latitude, wps[1].Longitude = near[0], near[1]
	s := build(t, line, wps, nil, 4000)
	st := s.Stops[0]
	if st.Kind != "stop" || st.Name != "" || st.AtM != nil {
		t.Fatalf("%+v", st)
	}
}

func TestElevationCollapsesCuts(t *testing.T) {
	line := eastLine(36.0, -115.0, 1000, 120)
	var elev [][2]int
	for d := 0; d <= 119000; d += 1000 {
		elev = append(elev, [2]int{d, 500 + d/1000})
	}
	s, err := Build(Input{Plan: planJSON(t, line, basicWps(line), elev), CutM: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if s.Elevation == nil || s.Elevation.Points[0][0] > 1000 {
		t.Fatalf("elevation: %+v", s.Elevation)
	}
	if len(s.Elevation.WaypointAtM) != len(s.Stops) {
		t.Fatal("waypoint_at_m misaligned")
	}
}
