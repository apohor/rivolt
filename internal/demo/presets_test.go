package demo

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/tripadvice"
	"github.com/apohor/rivolt/internal/tripmultiday"
)

var presetID = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// presetMaxBytes caps each preset (index entry with its shape, plus
// its plan) and each file.
const presetMaxBytes = 64 << 10

func readPresetIndex(t *testing.T) PresetIndex {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(PresetsDir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx PresetIndex
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// The files are the presets the contract describes: one plan file per
// index entry and nothing else, valid ids, plans in the planner's
// response shape, and summaries that agree with the plans.
func TestPresetsFiles(t *testing.T) {
	idx := readPresetIndex(t)
	if len(idx.Trips) < 5 {
		t.Fatalf("%d presets, want at least 5", len(idx.Trips))
	}
	want := []string{"index.json"}
	for _, p := range idx.Trips {
		want = append(want, p.ID+".plan.json")
		if !presetID.MatchString(p.ID) || p.Name == "" {
			t.Errorf("preset %q / %q", p.ID, p.Name)
		}
		b, err := os.ReadFile(filepath.Join(PresetsDir, p.ID+".plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		if entry, _ := json.Marshal(p); len(entry)+len(b) > presetMaxBytes {
			t.Errorf("%s: %d bytes with its plan, max %d", p.ID, len(entry)+len(b), presetMaxBytes)
		}
		if bytes.Contains(b, []byte(`"kind": "multiday"`)) {
			checkMultidayPreset(t, p, b)
			continue
		}
		var plan struct {
			*rivian.TripPlan
			Costs []tripadvice.CostEstimate `json:"costs"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&plan); err != nil {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if plan.TripPlan == nil || len(plan.Routes) != 1 || len(plan.Costs) != 1 {
			t.Fatalf("%s: plan %+v", p.ID, plan)
		}
		r, stops := plan.Routes[0], 0
		for _, w := range r.Waypoints {
			if w.EntityID != "" {
				t.Errorf("%s: waypoint %q carries an entity id", p.ID, w.Name)
			}
			if w.WaypointType == "DC_CHARGE_STATION" {
				stops++
			}
		}
		if s := p.Summary; s.ChargeStops != stops || s.ArrivalSoCPct != r.ArrivalSoC || s.DistanceMi <= 0 || s.DurationMin <= 0 {
			t.Errorf("%s: summary %+v for %d stops, arrival %v", p.ID, s, stops, r.ArrivalSoC)
		}
		checkShape(t, p.ID, p.RoutePolyline, r)
		// The planner can reopen it, and Plan vs Actual can read it.
		if _, err := ledger.ParsePlan(p.Inputs, b, presetPlansAt); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
	got, _ := filepath.Glob(filepath.Join(PresetsDir, "*"))
	for i := range got {
		// Served anonymously: keep each response small (the index holds
		// every preset's shape).
		if st, err := os.Stat(got[i]); err != nil {
			t.Error(err)
		} else if lim := int64(presetMaxBytes); filepath.Base(got[i]) == "index.json" && st.Size() > lim*int64(len(idx.Trips)) || filepath.Base(got[i]) != "index.json" && st.Size() > lim {
			t.Errorf("%s: %d bytes, max %d", got[i], st.Size(), presetMaxBytes)
		}
		got[i] = filepath.Base(got[i])
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("files %v, want %v", got, want)
	}
}

// The Las Vegas presets are the seeded saved trips (same ids) and are
// what the corpus builds now: a corpus change must regenerate them
// (go test -tags demotripsgen, see presets_gen_test.go).
func TestPresetsLasVegasAreTheSavedTrips(t *testing.T) {
	idx := readPresetIndex(t)
	byID := map[string]Preset{}
	for _, p := range idx.Trips {
		byID[p.ID] = p
	}
	vegas, err := vegasPresets()
	if err != nil {
		t.Fatal(err)
	}
	two, err := twoDayPreset([]string{byID[vegas[0].ID].RoutePolyline, byID[vegas[1].ID].RoutePolyline})
	if err != nil {
		t.Fatal(err)
	}
	vegas = append(vegas, two)
	ids := SampleTripIDs()
	if len(vegas) != len(ids) {
		t.Fatalf("%d Las Vegas presets for %d saved trips", len(vegas), len(ids))
	}
	for _, v := range vegas {
		if v.ID != ids[v.Name].String() {
			t.Errorf("%s: preset id %s, saved trip id %s", v.Name, v.ID, ids[v.Name])
		}
		p, ok := byID[v.ID]
		if !ok {
			t.Errorf("%s (%s) is not a preset", v.Name, v.ID)
			continue
		}
		b, err := os.ReadFile(filepath.Join(PresetsDir, v.ID+".plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		if compact(b) != compact(v.Plan) || p.Name != v.Name || compact(p.Inputs) != compact(v.Inputs) || p.Summary != v.Summary {
			t.Errorf("%s: the preset differs from what the corpus builds; regenerate", v.Name)
		}
	}
}

// checkShape checks that a route's map shape runs from its origin through
// every stop to its destination.
func checkShape(t *testing.T, id, polyline string, r rivian.TripRoute) {
	t.Helper()
	shape := decodePolyline6(polyline)
	near := func(lat, lon, km float64) bool {
		for _, q := range shape {
			if haversineM(q, [2]float64{lat, lon}) <= km*1000 {
				return true
			}
		}
		return false
	}
	// Every point stays near its own trip: inside the box of the route's waypoints, padded
	// half a degree (about 55 km). A polyline decoded at the wrong precision lands ten times
	// off and fails this, whatever region the preset is in. (This replaces a fixed
	// California/Nevada box, 8a03dbf4, which did the same job while every preset was there.)
	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, w := range r.Waypoints {
		minLat, maxLat = math.Min(minLat, w.Latitude), math.Max(maxLat, w.Latitude)
		minLon, maxLon = math.Min(minLon, w.Longitude), math.Max(maxLon, w.Longitude)
	}
	const pad = 0.5
	for _, q := range shape {
		if q[0] < minLat-pad || q[0] > maxLat+pad || q[1] < minLon-pad || q[1] > maxLon+pad {
			t.Fatalf("%s: polyline point %v is outside its trip's waypoints (padded %.1f°)", id, q, pad)
		}
	}
	if len(shape) < 10 || haversineM(shape[0], [2]float64{r.Waypoints[0].Latitude, r.Waypoints[0].Longitude}) > 500 ||
		haversineM(shape[len(shape)-1], [2]float64{r.Waypoints[len(r.Waypoints)-1].Latitude, r.Waypoints[len(r.Waypoints)-1].Longitude}) > 500 {
		t.Errorf("%s: shape of %d points doesn't run origin to destination", id, len(shape))
	}
	for _, w := range r.Waypoints {
		if !near(w.Latitude, w.Longitude, 1) {
			t.Errorf("%s: shape misses %q", id, w.Name)
		}
	}
}

// checkMultidayPreset checks a multi-day preset: its plan file is a saved
// multi-day snapshot whose days chain (each starts where the last ended,
// with an overnight between), each day a priced one-route plan with its own
// map shape that Plan vs Actual can read; the form has one overnight stop per
// night; the summary adds the days up.
func checkMultidayPreset(t *testing.T, p Preset, b []byte) {
	t.Helper()
	var snap struct {
		Kind    string          `json:"kind"`
		Request json.RawMessage `json:"request"`
		Days    []struct {
			Index int `json:"index"`
			Plan  struct {
				Routes []struct {
					rivian.TripRoute
					RoutePolyline string `json:"route_polyline"`
				}
			} `json:"plan"`
			DepartAt  time.Time                     `json:"depart_at"`
			Start     tripmultiday.Waypoint         `json:"start"`
			End       tripmultiday.Waypoint         `json:"end"`
			Overnight *tripmultiday.OvernightResult `json:"overnight"`
			Costs     []tripadvice.CostEstimate     `json:"costs"`
		} `json:"days"`
		Total tripmultiday.Totals `json:"total"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf("%s: %v", p.ID, err)
	}
	if snap.Kind != "multiday" || len(snap.Days) < 2 || len(snap.Request) == 0 {
		t.Fatalf("%s: snapshot kind %q, %d days", p.ID, snap.Kind, len(snap.Days))
	}
	var in struct {
		ExtraStops     []json.RawMessage `json:"extra_stops"`
		OvernightFlags []bool            `json:"overnight_flags"`
	}
	if err := json.Unmarshal(p.Inputs, &in); err != nil || len(in.ExtraStops) != len(snap.Days)-1 || len(in.OvernightFlags) != len(in.ExtraStops) {
		t.Errorf("%s: inputs %s for %d days", p.ID, p.Inputs, len(snap.Days))
	}
	if p.RoutePolyline != "" {
		t.Errorf("%s: a multi-day preset's shapes ride on its days", p.ID)
	}
	var stops int
	var meters float64
	for i, d := range snap.Days {
		if d.Index != i+1 || len(d.Plan.Routes) != 1 || len(d.Costs) != 1 {
			t.Fatalf("%s day %d: index %d, %d routes, %d costs", p.ID, i+1, d.Index, len(d.Plan.Routes), len(d.Costs))
		}
		r := d.Plan.Routes[0]
		for _, w := range r.Waypoints {
			if w.EntityID != "" {
				t.Errorf("%s day %d: waypoint %q carries an entity id", p.ID, i+1, w.Name)
			}
			if w.WaypointType == "DC_CHARGE_STATION" {
				stops++
			}
		}
		meters += r.TotalDriveDistanceMeters
		checkShape(t, p.ID, r.RoutePolyline, r.TripRoute)
		if last := i == len(snap.Days)-1; last != (d.Overnight == nil) {
			t.Errorf("%s day %d: overnight %+v", p.ID, i+1, d.Overnight)
		}
		if i > 0 {
			prev := snap.Days[i-1]
			if d.Start.Latitude != prev.End.Latitude || d.Start.Longitude != prev.End.Longitude || !d.DepartAt.After(prev.DepartAt) ||
				r.Waypoints[0].DepartureSoC != prev.Overnight.PostChargeSoCPct {
				t.Errorf("%s day %d doesn't leave from day %d's overnight", p.ID, i+1, i)
			}
		}
		// Plan vs Actual reads each day as the API builds it.
		dayIn, _ := json.Marshal(map[string]any{
			"origin":       map[string]any{"lat": d.Start.Latitude, "lon": d.Start.Longitude},
			"destination":  map[string]any{"lat": d.End.Latitude, "lon": d.End.Longitude},
			"departure_at": d.DepartAt.Format(time.RFC3339),
		})
		dayPlan, _ := json.Marshal(map[string]any{"Routes": []any{r.TripRoute}})
		if _, err := ledger.ParsePlan(dayIn, dayPlan, presetPlansAt); err != nil {
			t.Errorf("%s day %d: %v", p.ID, i+1, err)
		}
	}
	last := snap.Days[len(snap.Days)-1].Plan.Routes[0]
	if s := p.Summary; s.ChargeStops != stops || s.ArrivalSoCPct != last.ArrivalSoC || s.DistanceMi != math.Round(meters/1609.344*10)/10 ||
		s.DurationMin <= 0 || snap.Total.DistanceMeters != meters {
		t.Errorf("%s: summary %+v, total %+v for %d stops, %v m", p.ID, s, snap.Total, stops, meters)
	}
}

func compact(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return "invalid: " + err.Error()
	}
	return buf.String()
}

// decodePolyline6 decodes Valhalla's precision-6 encoded polyline.
func decodePolyline6(s string) [][2]float64 {
	var out [][2]float64
	var lat, lon int
	for i := 0; i < len(s); {
		var v [2]int
		for k := 0; k < 2; k++ {
			shift, res := 0, 0
			for i < len(s) {
				b := int(s[i]) - 63
				i++
				res |= (b & 0x1f) << shift
				shift += 5
				if b < 0x20 {
					break
				}
			}
			if res&1 != 0 {
				v[k] = ^(res >> 1)
			} else {
				v[k] = res >> 1
			}
		}
		lat += v[0]
		lon += v[1]
		out = append(out, [2]float64{float64(lat) / 1e6, float64(lon) / 1e6})
	}
	return out
}

func haversineM(a, b [2]float64) float64 {
	const r = 6371000.0
	la1, la2 := a[0]*math.Pi/180, b[0]*math.Pi/180
	dla, dlo := la2-la1, (b[1]-a[1])*math.Pi/180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Sqrt(h))
}
