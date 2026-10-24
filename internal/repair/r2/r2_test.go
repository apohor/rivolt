package r2

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

// rowsOf builds rows every step seconds from a compact script:
// (seconds, power, odo).
type step struct {
	sec   int
	power string
	odo   float64
}

func rowsOf(steps []step) []Row {
	out := make([]Row, len(steps))
	for i, s := range steps {
		out[i] = Row{At: at(s.sec), Power: s.power, OdoMi: 1000 + s.odo, SoC: 60 - s.odo/10, Lat: 34 + s.odo/100, Lon: -118}
	}
	return out
}

func TestTrips(t *testing.T) {
	cases := []struct {
		name  string
		steps []step
		want  []float64 // trip distances
	}{
		{"one trip through blank-power rows (resubscribes)", []step{
			{0, "ready", 0}, {60, "go", 0}, {120, "go", 0.6}, {150, "", 1.2}, {180, "", 1.9}, {240, "go", 2.5}, {300, "ready", 2.5}, {600, "ready", 2.5},
		}, []float64{2.5}},
		{"a pause under 2 min stays one trip", []step{
			{0, "go", 0}, {60, "go", 1}, {90, "ready", 1}, {150, "go", 1}, {210, "go", 2}, {500, "ready", 2},
		}, []float64{2}},
		{"parked 3 min: two trips", []step{
			{0, "go", 0}, {60, "go", 1}, {90, "ready", 1}, {200, "sleep", 1}, {300, "go", 1}, {360, "go", 3}, {700, "ready", 3},
		}, []float64{1, 2}},
		{"parked, then silent, then go: two trips", []step{
			{0, "go", 0}, {60, "go", 1}, {90, "ready", 1}, {1900, "go", 1}, {1960, "go", 1.5}, {2300, "ready", 1.5},
		}, []float64{1, 0.5}},
		{"a go run that never moved (sitting in drive) is no trip", []step{
			{0, "go", 5}, {60, "go", 5}, {120, "go", 5.05}, {500, "ready", 5.05},
		}, nil},
		{"blank rows before any go don't start a trip", []step{
			{0, "", 0}, {60, "", 0.5}, {120, "ready", 0.5},
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := Segment(rowsOf(c.steps))
			if len(got) != len(c.want) {
				t.Fatalf("%d trips (%+v), want %d", len(got), got, len(c.want))
			}
			for i, w := range c.want {
				if math.Abs(got[i].DistanceMi()-w) > 1e-9 {
					t.Errorf("trip %d: %.2f mi, want %.2f", i, got[i].DistanceMi(), w)
				}
			}
		})
	}

	trips, _ := Segment(rowsOf([]step{{0, "go", 0}, {600, "go", 5}, {660, "ready", 5}, {900, "ready", 5}}))
	tr := trips[0]
	if tr.Start != at(0) || tr.End != at(600) || math.Abs(tr.AvgSpeedMph()-30) > 1e-9 || len(tr.Path) != 2 {
		t.Errorf("trip fields: %+v", tr)
	}
	if e := tr.EnergyKWh(91.5); math.Abs(e-0.5/100*91.5) > 1e-9 {
		t.Errorf("energy %.3f", e)
	}
}

// A charge on an R2: power ramps up while the gear still says R; a 40-min
// gap starts a new charge; a telemetry gap can't invent energy.
func TestCharges(t *testing.T) {
	row := func(sec int, cs string, kw, soc float64) Row {
		return Row{At: at(sec), ChargingState: cs, KW: kw, SoC: soc, Lat: 34, Lon: -118}
	}
	rows := []Row{
		row(0, "charging_ready", 0, 38),
		row(179, "charging_active", 0, 38.1),
		row(184, "charging_active", 2.0, 38.1),
		row(209, "charging_active", 7.4, 38.2),
		row(209+1500, "charging_active", 7.4, 47), // 25 min at 7.4: the gap is capped at 10 min
		row(209+1500+600, "charging_active", 7.4, 49),
		row(209+1500+600+2400, "charging_active", 7.0, 49.1), // 40 min later: a new charge
		row(209+1500+600+2400+1800, "charging_active", 7.0, 54),
		row(209+1500+600+2400+1800+60, "charging_complete", 0, 54),
	}
	_, chs := Segment(rows)
	if len(chs) != 2 {
		t.Fatalf("%d charges, want 2: %+v", len(chs), chs)
	}
	// first: 5 s at 0, 25 s at 2.0, 10 min (capped) at 7.4, 10 min at 7.4
	want := 2.0*25/3600 + 7.4*(10.0/60) + 7.4*(10.0/60)
	if math.Abs(chs[0].EnergyKWh-want) > 1e-6 || chs[0].PeakKW != 7.4 || chs[0].StartSoC != 38.1 || chs[0].EndSoC != 49 {
		t.Errorf("first charge %+v, want %.3f kWh", chs[0], want)
	}
	if math.Abs(chs[1].EnergyKWh-7.0*(10.0/60)) > 1e-6 {
		t.Errorf("second charge %.3f kWh", chs[1].EnergyKWh)
	}
	// Under 1 kWh is noise.
	_, none := Segment([]Row{row(0, "charging_active", 0.5, 50), row(600, "charging_active", 0.5, 50)})
	if len(none) != 0 {
		t.Errorf("a 0.08 kWh run became a charge: %+v", none)
	}
}

func TestMakePlan(t *testing.T) {
	v := Vehicle{ID: uuid.New(), UserID: uuid.New(), RivianID: "01-000000001", Model: "R2", PackKWh: 91.47}
	since, until := at(0), at(86400)
	live := Existing{ExternalID: "live_x_d_1", Start: at(100), End: at(80000), Source: "live"}
	trips := []Trip{{Start: at(200), End: at(800), StartOdoMi: 1, EndOdoMi: 5}}
	chs := []Charge{{Start: at(1000), End: at(4000), EnergyKWh: 7}, {Start: at(9000), End: at(9500), EnergyKWh: 3}}
	stored := []Existing{{ExternalID: "live_x_c_9", Start: at(9100), End: at(9600)}}

	p, err := MakePlan(v, since, until, trips, chs, []Existing{live}, stored)
	if err != nil || len(p.DeleteDrives) != 1 || len(p.InsertDrives) != 1 || len(p.InsertCharges) != 1 || len(p.Skipped) != 1 {
		t.Fatalf("plan %+v err %v", p, err)
	}
	for name, d := range map[string]Existing{
		"not live":        {ExternalID: "imp", Start: at(100), End: at(200), Source: "import"},
		"crosses window":  {ExternalID: "x", Start: at(-10), End: at(200), Source: "live"},
		"user notes/tags": {ExternalID: "y", Start: at(100), End: at(200), Source: "live", UserEdited: true},
	} {
		if _, err := MakePlan(v, since, until, trips, chs, []Existing{d}, nil); err == nil {
			t.Errorf("%s: plan accepted", name)
		}
	}
	if id := ExternalID("01-000000001", "d", at(200)); !strings.HasPrefix(id, "live_01-000000001_d_") {
		t.Errorf("external id %q", id)
	}
}

// Only an R2: an R1's recorded gear is the better record.
func TestMakePlanRefusesR1(t *testing.T) {
	v := Vehicle{ID: uuid.New(), RivianID: "01-000000002", Model: "R1T"}
	if _, err := MakePlan(v, at(0), at(10), nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "not an R2") {
		t.Fatalf("R1 accepted: %v", err)
	}
}

// The first driving row may already be moving, and the parking row
// carries the final odometer: both ends fold in, so trip miles match the
// odometer (a truck can already have moved between a ready row and the
// first go row).
func TestTripEndsFoldParkedRows(t *testing.T) {
	trips, _ := Segment(rowsOf([]step{
		{0, "ready", 0}, {60, "go", 0.62}, {120, "go", 2}, {150, "ready", 2.3}, {400, "ready", 2.3},
	}))
	if len(trips) != 1 || math.Abs(trips[0].DistanceMi()-2.3) > 1e-9 {
		t.Fatalf("trips %+v, want one of 2.3 mi", trips)
	}
}

// Home AC at the home rate (as the recorder stamps it), away unpriced,
// DC fast never priced by a repair.
func TestPriceCharges(t *testing.T) {
	p := Pricing{HomeKnown: true, HomeLat: 34, HomeLon: -118, HomeRate: 0.12, Currency: "USD", HomeRadiusM: 250, DCFCMinKW: 50}
	got := PriceCharges([]Charge{
		{Lat: 34.0005, Lon: -118, EnergyKWh: 10, PeakKW: 7.4},
		{Lat: 34.1, Lon: -118, EnergyKWh: 10, PeakKW: 7.4},
		{Lat: 34, Lon: -118, EnergyKWh: 40, PeakKW: 150},
	}, p)
	if got[0].CostSource != "home_rate" || math.Abs(got[0].Cost-1.2) > 1e-9 || got[0].Place != "home" {
		t.Errorf("home AC: %+v", got[0])
	}
	if got[1].CostSource != "" || got[1].Place != "away" {
		t.Errorf("away AC: %+v", got[1])
	}
	if got[2].CostSource != "" {
		t.Errorf("DC fast at home priced: %+v", got[2])
	}
	// No saved home: the recorder can't tell it's away, so the home rate.
	if nh := PriceCharges([]Charge{{Lat: 34, Lon: -118, EnergyKWh: 5, PeakKW: 7}}, Pricing{HomeRate: 0.12, Currency: "USD", HomeRadiusM: 250, DCFCMinKW: 50}); nh[0].CostSource != "home_rate" || nh[0].Place != "" {
		t.Errorf("no home, rate set: %+v", nh[0])
	}
	// No rate: unpriced.
	if none := PriceCharges([]Charge{{Lat: 34, Lon: -118, EnergyKWh: 5}}, Pricing{HomeRadiusM: 250, DCFCMinKW: 50}); none[0].CostSource != "" {
		t.Errorf("no rate: %+v", none[0])
	}
}

// The synthetic corpus r2_park_drive_park_sleep (internal/rivian's replay harness,
// derived in shape from a real day and perturbed): its vehicleState rows give one
// trip of the corpus's own 5.15 mi of odometer.
func TestFixtureTrip(t *testing.T) {
	raw, err := os.ReadFile("../../rivian/testdata/corpus/r2_park_drive_park_sleep.json")
	if err != nil {
		t.Fatal(err)
	}
	var seq struct {
		Events []struct {
			T     float64  `json:"t"`
			Kind  string   `json:"kind"`
			Power string   `json:"power"`
			Odo   *float64 `json:"odo"`
			SoC   float64  `json:"soc"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &seq); err != nil {
		t.Fatal(err)
	}
	var rows []Row
	power, odo, soc := "", 0.0, 0.0
	for _, e := range seq.Events {
		if e.Kind != "vs" {
			continue
		}
		if e.Power != "" {
			power = e.Power
		}
		if e.Odo != nil {
			odo = *e.Odo
		}
		if e.SoC > 0 {
			soc = e.SoC
		}
		rows = append(rows, Row{At: t0.Add(time.Duration(e.T * float64(time.Second))), Power: power, OdoMi: 30000 + odo, SoC: soc})
	}
	trips, _ := Segment(rows)
	if len(trips) != 1 || math.Abs(trips[0].DistanceMi()-5.15) > 1e-9 {
		t.Fatalf("fixture: %d trips %+v, want one of 5.15 mi", len(trips), trips)
	}
}
