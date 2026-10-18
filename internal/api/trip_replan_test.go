package api

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/tripmultiday"
)

// The qa trip: Rose Bowl → Kettleman City (overnight, 3.3 kW) → San Francisco
// (overnight, no charger) → Rose Bowl via Kettleman City.
var (
	rpRoseBowl  = tripPoint{Lat: 34.1613, Lon: -118.1676, Label: "Rose Bowl"}
	rpKettleman = tripPoint{Lat: 35.9894, Lon: -119.9603, Label: "Kettleman City"}
	rpSF        = tripPoint{Lat: 37.7955, Lon: -122.3937, Label: "Ferry Building"}
	rpPT        = time.FixedZone("PDT", -7*3600)
)

func rpTrip() tripPlanMultidayRequest {
	slow, none := 3.3, 0.0
	return tripPlanMultidayRequest{
		Origin: rpRoseBowl, TZ: "America/Los_Angeles", DriveMode: "EVERYDAY",
		Days: []tripPlanDayRequest{
			{End: rpKettleman, Overnight: &tripPlanOvernight{L2KW: &slow, Until: "08:00", MaxSoCPct: 80, Name: "Kettleman City"}},
			{End: rpSF, Overnight: &tripPlanOvernight{L2KW: &none, Until: "08:00", Name: "Ferry Building"}},
			{Vias: []tripPoint{rpKettleman}, End: rpRoseBowl},
		},
	}
}

func rpAt(day, hh, mm int) time.Time { return time.Date(2026, 10, 19+day, hh, mm, 0, 0, rpPT) }

// rpSaved is the trip as planned: each day leaves at 08:00 and drives 4 h.
func rpSaved() []replanSavedDay {
	ends := []tripPoint{rpKettleman, rpSF, rpRoseBowl}
	out := make([]replanSavedDay, 3)
	for i := range out {
		out[i] = replanSavedDay{DepartAt: rpAt(i+1, 8, 0), ArriveAt: rpAt(i+1, 12, 0), End: ends[i].waypoint()}
	}
	return out
}

func TestReplanNotStarted(t *testing.T) {
	p, err := buildReplan(rpTrip(), rpSaved(), replanState{Lat: 34.15, Lon: -118.14, SoC: 88, At: rpAt(1, 7, 0)}, 131)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "not_started" || p.FromDay != 1 || len(p.Req.Days) != 3 {
		t.Fatalf("got phase %s from day %d with %d days", p.Phase, p.FromDay, len(p.Req.Days))
	}
	if p.Req.Origin.Lat != 34.15 || *p.Req.StartingSoC != 88 || p.Req.StartAt != "2026-10-20T07:00:00-07:00" {
		t.Errorf("origin %+v soc %v start %s", p.Req.Origin, *p.Req.StartingSoC, p.Req.StartAt)
	}
}

func TestReplanDrivingMidDay(t *testing.T) {
	// Day 2 at noon, near Gilroy, heading for San Francisco.
	p, err := buildReplan(rpTrip(), rpSaved(), replanState{Lat: 37.00, Lon: -121.57, SoC: 40, At: rpAt(2, 12, 0).Add(-time.Hour)}, 131)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "driving" || p.FromDay != 2 || len(p.Req.Days) != 2 {
		t.Fatalf("got phase %s from day %d with %d days", p.Phase, p.FromDay, len(p.Req.Days))
	}
	if p.Req.Days[0].End != rpSF || p.Req.Days[0].DepartAt != "" || p.Req.Days[1].End != rpRoseBowl {
		t.Errorf("remaining days wrong: %+v", p.Req.Days)
	}
	if len(p.Req.Days[1].Vias) != 1 {
		t.Errorf("a later day's vias must be kept: %+v", p.Req.Days[1].Vias)
	}
}

func TestReplanDropsPassedViasUnlessOverridden(t *testing.T) {
	// Day 3, south of Kettleman City: the via is behind the truck.
	st := replanState{Lat: 35.30, Lon: -119.05, SoC: 50, At: rpAt(3, 10, 0)}
	p, err := buildReplan(rpTrip(), rpSaved(), st, 131)
	if err != nil {
		t.Fatal(err)
	}
	if p.FromDay != 3 || len(p.ViasDropped) != 1 || len(p.Req.Days[0].Vias) != 0 {
		t.Fatalf("from day %d dropped %v vias %v", p.FromDay, p.ViasDropped, p.Req.Days[0].Vias)
	}
	// Still north of it: kept.
	st.Lat, st.Lon = 36.50, -120.40
	if p, _ = buildReplan(rpTrip(), rpSaved(), st, 131); len(p.Req.Days[0].Vias) != 1 {
		t.Errorf("a via ahead of the truck was dropped: %v", p.ViasDropped)
	}
	// The user says it's done.
	st.ViasDone = []int{0}
	if p, _ = buildReplan(rpTrip(), rpSaved(), st, 131); len(p.Req.Days[0].Vias) != 0 {
		t.Errorf("vias_done not applied")
	}
	st.ViasDone = []int{3}
	if _, err := buildReplan(rpTrip(), rpSaved(), st, 131); err == nil || err.status != http.StatusBadRequest {
		t.Errorf("a vias_done index past the day's vias must be a 400, got %v", err)
	}
}

func TestReplanOvernightProjectsTheRestOfTheStay(t *testing.T) {
	// Parked at Kettleman City at 20:00 on day 1 with 30 %.
	st := replanState{Lat: rpKettleman.Lat, Lon: rpKettleman.Lon, SoC: 30, At: rpAt(1, 20, 0)}
	p, err := buildReplan(rpTrip(), rpSaved(), st, 131)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "overnight" || p.FromDay != 2 || len(p.Req.Days) != 2 {
		t.Fatalf("got phase %s from day %d with %d days", p.Phase, p.FromDay, len(p.Req.Days))
	}
	if p.Req.StartAt != rpAt(2, 8, 0).Format(time.RFC3339) {
		t.Errorf("day 2 must keep its planned 08:00 departure, got %s", p.Req.StartAt)
	}
	slow := 3.3
	want := tripmultiday.OvernightCharge(30, 12, tripmultiday.Overnight{L2KW: &slow, MaxSoCPct: 80}, 131, 0, 80).PostChargeSoCPct
	if math.Abs(*p.Req.StartingSoC-want) > 1e-9 || want <= 30 || p.Overnight == nil {
		t.Errorf("start SoC %v, want %v (30 + 12 h at 3.3 kW)", *p.Req.StartingSoC, want)
	}
}

func TestReplanTripComplete(t *testing.T) {
	st := replanState{Lat: rpRoseBowl.Lat, Lon: rpRoseBowl.Lon, SoC: 20, At: rpAt(3, 13, 0)}
	if _, err := buildReplan(rpTrip(), rpSaved(), st, 131); err == nil || err.class != "trip_complete" {
		t.Fatalf("want trip_complete, got %v", err)
	}
}

func TestReplanFromDayOverride(t *testing.T) {
	st := replanState{Lat: 36.0, Lon: -120.0, SoC: 60, At: rpAt(1, 9, 0), FromDay: 2}
	p, err := buildReplan(rpTrip(), rpSaved(), st, 131)
	if err != nil || p.FromDay != 2 || p.Phase != "driving" {
		t.Fatalf("from_day override: %+v %v", p, err)
	}
	st.FromDay = 4
	if _, err := buildReplan(rpTrip(), rpSaved(), st, 131); err == nil || err.status != http.StatusBadRequest {
		t.Errorf("from_day past the trip must be a 400")
	}
}

func TestReconstructRequestMatchesThePlannerForm(t *testing.T) {
	var f replanForm
	if err := json.Unmarshal([]byte(`{
	  "origin": {"lat": 34.1613, "lon": -118.1676, "label": "Rose Bowl"},
	  "destination": {"lat": 34.1613, "lon": -118.1676, "label": "Rose Bowl"},
	  "extra_stops": [
	    {"lat": 35.9894, "lon": -119.9603, "label": "Kettleman City"},
	    {"lat": 37.0, "lon": -121.57, "label": "Gilroy"},
	    {"lat": 37.7955, "lon": -122.3937, "label": "Ferry Building"}],
	  "overnight_flags": [true, false, true],
	  "overnight_departure_local": "08:00", "max_overnight_soc_pct": 80, "target_soc": "15"}`), &f); err != nil {
		t.Fatal(err)
	}
	req, ok := reconstructRequest(f)
	if !ok || len(req.Days) != 3 {
		t.Fatalf("got %d days", len(req.Days))
	}
	d0, d1, d2 := req.Days[0], req.Days[1], req.Days[2]
	if d0.End.Label != "Kettleman City" || len(d0.Vias) != 0 || d0.Overnight == nil || *d0.Overnight.L2KW != tripmultiday.DefaultL2KW || d0.Overnight.Until != "08:00" {
		t.Errorf("day 1: %+v %+v", d0, d0.Overnight)
	}
	if d1.End.Label != "Ferry Building" || len(d1.Vias) != 1 || d1.Vias[0].Label != "Gilroy" || d1.Overnight == nil {
		t.Errorf("day 2: %+v", d1)
	}
	if d2.End.Label != "Rose Bowl" || d2.Overnight != nil || *d2.TargetArrivalSocPct != 15 {
		t.Errorf("day 3: %+v", d2)
	}
}

func TestWithStatusSetsStatusAndIndex(t *testing.T) {
	b, err := withStatus(json.RawMessage(`{"index":1,"x":2}`), "replanned", 3)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["status"] != "replanned" || m["index"] != float64(3) || m["x"] != float64(2) {
		t.Errorf("got %s", b)
	}
}
