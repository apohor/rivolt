package insights

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

var seq int

// drive builds a drive starting n hours after t0 (so drives never overlap
// unless asked), 30 minutes long.
func drive(n int, mi, kwh, mph float64, tempC *float64, mode string) DriveInput {
	seq++
	start := t0.Add(time.Duration(n) * time.Hour)
	return DriveInput{ID: "d" + string(rune('a'+seq%26)) + time.Duration(seq).String(), VehicleID: "v",
		StartedAt: start, EndedAt: start.Add(30 * time.Minute), Source: "live",
		DistanceMi: mi, EnergyKWh: kwh, AvgSpeedMph: mph, OutsideTempC: tempC, DriveMode: mode}
}

func c(v float64) *float64 { return &v }

func byKey(bs []Bucket, key string) Bucket {
	for _, b := range bs {
		if b.Key == key {
			return b
		}
	}
	return Bucket{}
}

func eff(t *testing.T, b Bucket) float64 {
	t.Helper()
	if b.Efficiency == nil {
		t.Fatalf("%s: no efficiency", b.Key)
	}
	return *b.Efficiency
}

func TestEfficiencyIsWeightedByDistance(t *testing.T) {
	// 10 mi on 5 kWh (2.0) and 100 mi on 25 kWh (4.0): by distance 110/30 =
	// 3.67, not the per-drive average 3.0.
	e := BuildEfficiency([]DriveInput{
		drive(0, 10, 5, 30, c(5), "everyday"),
		drive(1, 100, 25, 60, c(5), "everyday"),
	}, Imperial)
	if got := eff(t, e.Total); math.Abs(got-3.67) > 0.005 {
		t.Errorf("total = %v mi/kWh, want 3.67", got)
	}
	if b := byKey(e.ByTemperature, "0_10c"); b.Drives != 2 || math.Abs(eff(t, b)-3.67) > 0.005 || b.Distance != 110 || b.EnergyKWh != 30 {
		t.Errorf("0–10 °C bucket = %+v", b)
	}
}

func TestEfficiencyBuckets(t *testing.T) {
	e := BuildEfficiency([]DriveInput{
		drive(0, 30, 12, 25, c(-3), "winter"),  // below 0, city
		drive(1, 30, 10, 35, c(0), "everyday"), // 0 °C goes to 0–10; 35 mph to mixed
		drive(2, 30, 9, 60, c(25), "distance"), // 20–30, highway
		drive(3, 30, 11, 75, c(31), "sport"),   // 30+, fast highway
		drive(4, 30, 10, 0, c(15), "everyday"), // no avg speed: 30 mi in 0.5 h = 60 mph
		drive(5, 30, 10, 45, nil, ""),          // no temperature, no mode
	}, Imperial)
	want := map[string]int{"below_0c": 1, "0_10c": 1, "10_20c": 1, "20_30c": 1, "30c_up": 1}
	for k, n := range want {
		if b := byKey(e.ByTemperature, k); b.Drives != n {
			t.Errorf("temperature %s: %d drives, want %d", k, b.Drives, n)
		}
	}
	if len(e.ByTemperature) != 5 || e.ByTemperature[0].Label != "Below 32 °F" || e.ByTemperature[4].Label != "86 °F and above" {
		t.Errorf("temperature rows = %+v", e.ByTemperature)
	}
	sp := map[string]int{"city": 1, "mixed": 2, "highway": 2, "fast_highway": 1}
	for k, n := range sp {
		if b := byKey(e.BySpeed, k); b.Drives != n {
			t.Errorf("speed %s: %d drives, want %d", k, b.Drives, n)
		}
	}
	if e.Excluded.NoTemperature != 1 || e.Excluded.NoDriveMode != 1 || e.Total.Drives != 6 {
		t.Errorf("excluded = %+v, total drives %d", e.Excluded, e.Total.Drives)
	}
	// Modes: labelled as in the app, most distance first (everyday 60 mi).
	if e.ByDriveMode[0].Key != "everyday" || e.ByDriveMode[0].Label != "All-Purpose" || e.ByDriveMode[0].Drives != 2 {
		t.Errorf("first mode = %+v", e.ByDriveMode[0])
	}
	if b := byKey(e.ByDriveMode, "distance"); b.Label != "Conserve" {
		t.Errorf("distance mode label = %q", b.Label)
	}
}

func TestEfficiencyNotEnoughData(t *testing.T) {
	e := BuildEfficiency([]DriveInput{
		drive(0, 21, 8, 30, c(5), "everyday"),
		drive(1, 4, 2, 30, c(25), "everyday"),
	}, Imperial)
	if b := byKey(e.ByTemperature, "20_30c"); !b.NotEnoughData || b.Efficiency == nil {
		t.Errorf("4 mi bucket: %+v, want flagged but still computed", b)
	}
	if b := byKey(e.ByTemperature, "30c_up"); !b.NotEnoughData || b.Efficiency != nil || b.Drives != 0 {
		t.Errorf("empty bucket: %+v", b)
	}
	if e.Total.NotEnoughData {
		t.Error("a 25 mi total flagged: 25 mi is enough")
	}
	if b := byKey(e.ByTemperature, "0_10c"); !b.NotEnoughData {
		t.Error("a 21 mi bucket isn't flagged")
	}
}

func TestEfficiencyExcludesGlitchesAndDuplicates(t *testing.T) {
	a := drive(0, 40, 16, 40, c(5), "everyday")
	dup := a // the same drive recorded twice (an import beside the live row)
	dup.ID, dup.Source, dup.EnergyKWh = "dup", "import", 0
	e := BuildEfficiency([]DriveInput{
		a, dup,
		drive(1, 5, 0, 30, c(5), "everyday"),    // no energy
		drive(2, 30, 0.5, 30, c(5), "everyday"), // 60 mi/kWh: glitch
		drive(3, 0.2, 2, 30, c(5), "everyday"),  // 0.1 mi/kWh: glitch
	}, Imperial)
	if e.Total.Drives != 1 || e.Total.Distance != 40 {
		t.Errorf("total = %+v", e.Total)
	}
	if e.Excluded.Duplicates != 1 || e.Excluded.NoEnergy != 3 {
		t.Errorf("excluded = %+v, want 1 duplicate, 3 without usable energy", e.Excluded)
	}
}

func TestEfficiencyMetric(t *testing.T) {
	e := BuildEfficiency([]DriveInput{drive(0, 100, 25, 60, c(-5), "everyday")}, Metric)
	if e.DistanceUnit != "km" || e.EfficiencyUnit != "km/kWh" || e.MinBucketDistance != 40 {
		t.Errorf("units = %s, %s, min %v", e.DistanceUnit, e.EfficiencyUnit, e.MinBucketDistance)
	}
	if got := eff(t, e.Total); math.Abs(got-6.44) > 0.005 { // 160.93 km / 25 kWh
		t.Errorf("total = %v km/kWh, want 6.44", got)
	}
	if e.Total.Distance != 160.9 {
		t.Errorf("distance = %v km", e.Total.Distance)
	}
	if e.ByTemperature[0].Label != "Below 0 °C" || byKey(e.BySpeed, "highway").Label != "Highway (89–113 km/h)" {
		t.Errorf("metric labels: %q, %q", e.ByTemperature[0].Label, byKey(e.BySpeed, "highway").Label)
	}
}

func TestEfficiencyEmpty(t *testing.T) {
	e := BuildEfficiency(nil, "")
	if e.Units != Imperial || e.Total.Drives != 0 || e.Total.Efficiency != nil || len(e.ByTemperature) != 5 ||
		len(e.BySpeed) != 4 || e.ByDriveMode == nil {
		t.Errorf("empty = %+v", e)
	}
}
