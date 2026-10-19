package api

import "testing"

func mkColdDrives(temp float64, effs ...float64) []coldDrive {
	var out []coldDrive
	for _, e := range effs {
		out = append(out, coldDrive{TempC: temp, Eff: e, Mi: 20})
	}
	return out
}

func TestColdBandIsTheMiddleHalfByDistance(t *testing.T) {
	// Eight equal drives at -7 °C: 25th and 75th percentile are the 2nd and 6th.
	ds := mkColdDrives(-7, 1.6, 1.7, 1.8, 1.9, 2.0, 2.1, 2.2, 2.3)
	b := coldBandFor(ds, -6.7)
	if b.Insufficient || b.Drives != 8 || b.Miles != 160 {
		t.Fatalf("band = %+v", b)
	}
	if b.EffLow != 1.7 || b.EffHigh != 2.1 {
		t.Errorf("band = %v..%v, want 1.7..2.1", b.EffLow, b.EffHigh)
	}
	if b.LoC != -6.7-coldWindowC || b.HiC != -6.7+coldWindowC {
		t.Errorf("window = %v..%v, want the narrow window", b.LoC, b.HiC)
	}
}

func TestColdQuantileWeighsDistance(t *testing.T) {
	// One long drive at 3.0 outweighs four short ones at 2.0.
	sel := []coldDrive{{Eff: 2.0, Mi: 5}, {Eff: 2.0, Mi: 5}, {Eff: 2.0, Mi: 5}, {Eff: 2.0, Mi: 5}, {Eff: 3.0, Mi: 100}}
	if got := coldQuantile(sel, 0.25); got != 3.0 {
		t.Errorf("q25 = %v, want 3.0 (the long drive holds most of the miles)", got)
	}
}

func TestColdWindowWidensOnceThenGivesUp(t *testing.T) {
	// Five drives 5 °C away: outside the narrow window, inside the wide one.
	ds := mkColdDrives(-1.7, 2, 2, 2, 2, 2)
	b := coldBandFor(ds, -6.7)
	if b.Insufficient || b.LoC != -6.7-coldWideWindowC {
		t.Fatalf("band = %+v, want the wide window", b)
	}
	// 10 °C away: never used, no extrapolation.
	if b := coldBandFor(ds, -11.7-3); !b.Insufficient {
		t.Errorf("a temperature far from every drive got a figure: %+v", b)
	}
}

func TestColdNeedsDrivesAndMiles(t *testing.T) {
	few := mkColdDrives(0, 2, 2, 2, 2) // four drives, 80 mi
	if !coldBandFor(few, 0).Insufficient {
		t.Error("four drives gave a figure")
	}
	short := []coldDrive{{Eff: 2, Mi: 5}, {Eff: 2, Mi: 5}, {Eff: 2, Mi: 5}, {Eff: 2, Mi: 5}, {Eff: 2, Mi: 5}} // 25 mi
	if !coldBandFor(short, 0).Insufficient {
		t.Error("25 miles gave a figure")
	}
}

func TestParseColdGrid(t *testing.T) {
	if g, ok := parseColdGrid("-17.8, -6.7,4.4"); !ok || len(g) != 3 || g[1] != -6.7 {
		t.Errorf("grid = %v %v", g, ok)
	}
	for _, bad := range []string{"1,2,3,4,5,6,7,8,9", "x", "100", "1,,2"} {
		if _, ok := parseColdGrid(bad); ok {
			t.Errorf("grid %q accepted", bad)
		}
	}
	if g, ok := parseColdGrid(""); !ok || g != nil {
		t.Errorf("empty grid = %v %v", g, ok)
	}
}
