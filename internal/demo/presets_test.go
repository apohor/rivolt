package demo

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/tripadvice"
)

var presetID = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// presetMaxBytes caps each preset file.
const presetMaxBytes = 16 << 10

func readPresets(t *testing.T) PresetIndex {
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
	idx := readPresets(t)
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
		// The planner can reopen it, and Plan vs Actual can read it.
		if _, err := ledger.ParsePlan(p.Inputs, b, presetPlansAt); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
	got, _ := filepath.Glob(filepath.Join(PresetsDir, "*"))
	for i := range got {
		// Served anonymously: keep each response small.
		if st, err := os.Stat(got[i]); err != nil {
			t.Error(err)
		} else if st.Size() > presetMaxBytes {
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

// The Las Vegas presets are the seeded saved trips (linked by name) and are
// what the corpus builds now: a corpus change must regenerate them
// (go test -tags demotripsgen, see presets_gen_test.go).
func TestPresetsLasVegasAreTheSavedTrips(t *testing.T) {
	idx := readPresets(t)
	byID := map[string]Preset{}
	for _, p := range idx.Trips {
		byID[p.ID] = p
	}
	vegas, err := vegasPresets()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, n := range SampleTripNames() {
		names[n] = true
	}
	linked := 0
	for _, p := range idx.Trips {
		if p.SavedTripName != "" {
			linked++
			if !names[p.SavedTripName] {
				t.Errorf("%s links to %q, not a seeded saved trip", p.ID, p.SavedTripName)
			}
		}
	}
	if len(vegas) != len(names) || linked != len(names) {
		t.Fatalf("%d Las Vegas presets, %d linked, for %d saved trips", len(vegas), linked, len(names))
	}
	for _, v := range vegas {
		p, ok := byID[v.ID]
		if !ok {
			t.Errorf("%s (%s) is not a preset", v.Name, v.ID)
			continue
		}
		b, err := os.ReadFile(filepath.Join(PresetsDir, v.ID+".plan.json"))
		if err != nil {
			t.Fatal(err)
		}
		if compact(b) != compact(v.Plan) || p.Name != v.Name || p.SavedTripName != v.SavedTripName || compact(p.Inputs) != compact(v.Inputs) || p.Summary != v.Summary {
			t.Errorf("%s: the preset differs from what the corpus builds; regenerate", v.Name)
		}
	}
}

func compact(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return "invalid: " + err.Error()
	}
	return buf.String()
}
