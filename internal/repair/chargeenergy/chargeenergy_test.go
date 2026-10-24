package chargeenergy

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func f(v float64) *float64 { return &v }

// base is a charge that lost most of its energy: 30 % SoC on a 100 kWh pack (32.4 kWh at the pack-based estimate), 5 kWh
// recorded, priced at 0.20 from the home rate.
func base() Row {
	start := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	return Row{ID: uuid.New(), UserID: uuid.New(), VehicleID: uuid.New(), Model: "R1T", PackKWh: 100, StartedAt: start, EndedAt: start.Add(8 * time.Hour),
		StartSoC: 20, EndSoC: 50, Energy: 5, Cost: f(1), Price: f(0.2), CostSource: "home_rate", MaxPowerKW: 7.5, Source: "live"}
}

func TestEstimate(t *testing.T) {
	if e := Estimate(100, nil); e.Method != "pack x loss factor" || math.Abs(e.KWh-1.08) > 1e-9 {
		t.Errorf("no clean charges: %+v, want the pack-based 1.08 kWh/%%", e)
	}
	if e := Estimate(100, []float64{1.3, 1.4}); e.Method != "pack x loss factor" {
		t.Errorf("two clean charges are too few: %+v", e)
	}
	if e := Estimate(100, []float64{1.5, 1.3, 1.4}); math.Abs(e.KWh-1.4) > 1e-9 || e.Method != "own median of 3 clean charges" {
		t.Errorf("three: %+v, want the median 1.4", e)
	}
	if e := Estimate(100, []float64{1.3, 1.5, 1.4, 1.6}); math.Abs(e.KWh-1.45) > 1e-9 {
		t.Errorf("four: %+v, want the mean of the two middle ones, 1.45", e)
	}
}

func TestClassify(t *testing.T) {
	est := Estimate(100, nil) // 1.08 per %: a 30 % gain should be 32.4 kWh
	cases := []struct {
		name   string
		mut    func(*Row)
		repair bool
	}{
		{"an integral that lost most of it", func(r *Row) {}, true},
		{"just under the ratio", func(r *Row) { r.Energy = 25.9 }, true},
		{"just over the ratio is variance", func(r *Row) { r.Energy = 26 }, false},
		{"in the 80 to 90 % band", func(r *Row) { r.Energy = 28.5 }, false},
		{"a fast charge", func(r *Row) { r.MaxPowerKW = 150 }, false},
		{"a user's own price", func(r *Row) { r.CostSource = "user" }, false},
		{"Rivian's billed price", func(r *Row) { r.CostSource = "rivian" }, false},
		{"a confirmed network", func(r *Row) { r.NetworkState = "confirmed" }, false},
		{"a place the user set", func(r *Row) { r.PlaceOverride = "home" }, false},
		{"a location the user set", func(r *Row) { r.LocationOverride = "recorded" }, false},
		{"already estimated", func(r *Row) { r.Metadata = []byte(`{"energy_estimated": true}`) }, false},
		{"a small SoC gain", func(r *Row) { r.EndSoC = 24 }, false},
		{"a start SoC of 0 (unknown) is not a 50 % gain", func(r *Row) { r.StartSoC = 0 }, false},
		{"an end SoC over 100", func(r *Row) { r.EndSoC = 140 }, false},
		{"an SoC that fell", func(r *Row) { r.StartSoC, r.EndSoC = 50, 20 }, false},
		{"an estimate over peak power x duration", func(r *Row) { r.EndedAt = r.StartedAt.Add(4 * time.Hour) }, false}, // 7.5 kW x 4 h x 1.05 = 31.5 < 32.4
		{"just inside peak power x duration", func(r *Row) { r.EndedAt = r.StartedAt.Add(4*time.Hour + 20*time.Minute) }, true},
		{"no duration", func(r *Row) { r.EndedAt = r.StartedAt }, false},
		{"priced by a network rate", func(r *Row) { r.CostSource = "network_estimate" }, false},
		{"priced by an observed estimate", func(r *Row) { r.CostSource = "observed_estimate" }, false},
		{"an unpriced row (no cost source) is repaired", func(r *Row) { r.CostSource, r.Cost, r.Price = "", nil, nil }, true},
		{"not a live row", func(r *Row) { r.Source = "import" }, false},
	}
	for _, c := range cases {
		r := base()
		c.mut(&r)
		if v := Classify(r, est); v.Repair != c.repair {
			t.Errorf("%s: repair=%v reason=%q, want %v", c.name, v.Repair, v.Reason, c.repair)
		}
	}
	v := Classify(base(), est)
	if math.Abs(v.NewEnergy-32.4) > 1e-9 || v.NewCost == nil || math.Abs(*v.NewCost-6.48) > 1e-9 {
		t.Errorf("verdict %+v, want 32.4 kWh and a cost of 6.48 at the row's own 0.20", v)
	}
	// An unpriced row keeps no cost to invent; a row priced some other way keeps its cost.
	r := base()
	r.Cost, r.Price = nil, nil
	if v := Classify(r, est); !v.Repair || v.NewCost != nil {
		t.Errorf("unpriced: %+v, want the energy repaired and no cost", v)
	}
	if v := Classify(r, est); !v.Repair || v.NewCost != nil {
		t.Errorf("an unpriced row: %+v, want the energy repaired and no cost", v)
	}
	// It only ever raises.
	r = base()
	r.Energy = 40
	if v := Classify(r, Estimate(100, []float64{0.2, 0.2, 0.2})); v.Repair {
		t.Errorf("a lower estimate lowered a row: %+v", v)
	}
}

func TestIsClean(t *testing.T) {
	r := base()
	r.Energy = 32 // 1.07 kWh/% on a pack expecting 1.08
	if !IsClean(r) {
		t.Error("a normal AC charge is not clean")
	}
	for name, mut := range map[string]func(*Row){
		"a reset row": func(r *Row) { r.Energy = 5 }, "too high": func(r *Row) { r.Energy = 60 }, "a fast charge": func(r *Row) { r.MaxPowerKW = 150 },
		"a small gain": func(r *Row) { r.EndSoC = 23 }, "an unknown pack": func(r *Row) { r.PackKWh = 0 },
		"a start of 0": func(r *Row) { r.StartSoC = 0 }, "an end over 100": func(r *Row) { r.EndSoC = 140 },
	} {
		x := r
		mut(&x)
		if IsClean(x) {
			t.Errorf("%s counted as clean", name)
		}
	}
}

func TestSummariseCountsPerModelWithoutNamingAnyone(t *testing.T) {
	a, b, c := base(), base(), base()
	b.UserID = a.UserID // two rows, one account
	c.Model, c.PackKWh = "R2", 91.5
	c.Energy = 30 // fine
	own := Estimate(100, []float64{1.4, 1.4, 1.4})
	plan := []Planned{
		{Row: a, Est: Estimate(100, nil), Verdict: Classify(a, Estimate(100, nil))},
		{Row: b, Est: own, Verdict: Classify(b, own)},
		{Row: c, Est: Estimate(91.5, nil), Verdict: Classify(c, Estimate(91.5, nil))},
	}
	got := Summarise(plan)
	if len(got) != 2 || got[0].Model != "R1T" || got[0].Considered != 2 || got[0].Repair != 2 || got[0].Accounts != 1 || got[0].OwnMedian != 1 || got[0].PackBased != 1 {
		t.Errorf("R1T summary: %+v", got)
	}
	if got[1].Model != "R2" || got[1].Considered != 1 || got[1].Repair != 0 {
		t.Errorf("R2 summary: %+v", got[1])
	}
}

func TestBackupRoundTripAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.jsonl")
	in := []BackupRow{{ID: uuid.New(), UserID: uuid.New(), Energy: 5, Cost: f(1), Metadata: []byte(`{"a":1}`), NewEnergy: 32.4, NewCost: f(6.48), Method: "pack x loss factor"}}
	if err := WriteBackup(path, in); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackup(path, in); err == nil {
		t.Error("a second write overwrote the backup")
	}
	out, err := ReadBackup(path)
	if err != nil || len(out) != 1 || out[0].ID != in[0].ID || out[0].Energy != 5 || *out[0].NewCost != 6.48 || string(out[0].Metadata) != `{"a":1}` {
		t.Errorf("round trip: %+v %v", out, err)
	}
}
