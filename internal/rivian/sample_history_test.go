package rivian

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"
	"time"
)

// A date well past the corpus's first recorded row on preview, the usual boundary.
var sampleHistTestBefore = time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

// The public demo's frames are exactly what they were before the older history existed: the history planner shares the builder, and its
// only hook (effMi) is off for the corpus.
func TestSampleCorpusUnchanged(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := NewSampleCorpus(SampleSeed).WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	const want = "a5e0d9c235acad8b0c9daf1bcc5e3f6a82ee6e4cb62b74f361899f13f8270330"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("the demo corpus changed: sha256 %s, want %s", got, want)
	}
}

func TestSampleHistory_CoversOverFifteenMonthsAndIsStable(t *testing.T) {
	t.Parallel()
	h := NewSampleHistory(sampleHistTestBefore) // must not panic: the battery never runs flat
	if len(h.Drives) == 0 || len(h.Charges) == 0 {
		t.Fatal("empty history")
	}
	first, last := h.Drives[0].Start, h.Drives[len(h.Drives)-1].End
	if last.After(sampleHistTestBefore) {
		t.Errorf("history runs past its boundary: %v", last)
	}
	// Today is at least 90 days past the boundary on preview; with the 15-month floor, the history must reach 12 months before it.
	if span := last.Sub(first); span < 15*30*24*time.Hour {
		t.Errorf("history spans %.0f days, want 15 months or more", span.Hours()/24)
	}
	var mi, kwh, cost float64
	months := map[string]float64{}
	for _, d := range h.Drives {
		mi += d.Miles
		months[d.Start.Format("2006-01")] += d.Miles
		if d.Miles <= 0 || d.EnergyKWh <= 0 || !d.End.After(d.Start) || d.Polyline == "" {
			t.Fatalf("bad drive %+v", d)
		}
	}
	for m, v := range months {
		if v < 50 { // the trends card needs 50 mi in a month
			t.Errorf("month %s has only %.0f mi", m, v)
		}
	}
	dc, home := 0, 0
	for _, c := range h.Charges {
		kwh += c.EnergyKWh
		cost += c.Cost
		if c.DCFC {
			dc++
		}
		if c.Place == "home" {
			home++
		}
		if c.EnergyKWh <= 0 || c.Cost <= 0 || c.CostSource == "" {
			t.Fatalf("bad charge %+v", c)
		}
	}
	if h.Trips < 6 || dc < 20 || home < 100 {
		t.Errorf("trips %d, fast charges %d, home charges %d: not a believable mix", h.Trips, dc, home)
	}
	if mi < 14000 || mi > 22000 || kwh < 6000 || kwh > 10000 || cost < 1200 || cost > 2600 {
		t.Errorf("totals off: %.0f mi, %.0f kWh, $%.0f", mi, kwh, cost)
	}
	if e := mi / kwh; e < 2.0 || e > 2.6 {
		t.Errorf("overall %.2f mi/kWh", e)
	}
	// Exactly the same rows for the same boundary; a later boundary only adds.
	h2 := NewSampleHistory(sampleHistTestBefore)
	if len(h2.Drives) != len(h.Drives) || len(h2.Charges) != len(h.Charges) || h2.Drives[17] != h.Drives[17] {
		t.Error("same boundary, different history")
	}
	h3 := NewSampleHistory(sampleHistTestBefore.AddDate(0, 0, 20))
	if len(h3.Drives) <= len(h.Drives) {
		t.Fatal("a later boundary added nothing")
	}
	for i := range h.Drives {
		if h3.Drives[i] != h.Drives[i] {
			t.Fatalf("drive %d changed when the boundary moved", i)
		}
	}
	for i := range h.Charges {
		if h3.Charges[i] != h.Charges[i] {
			t.Fatalf("charge %d changed when the boundary moved", i)
		}
	}
}

// Colder months run a lower mi/kWh (winter efficiency below summer's), which is what the seasons card and cold-range read.
func TestSampleHistory_WinterIsLessEfficientAndColder(t *testing.T) {
	t.Parallel()
	h := NewSampleHistory(sampleHistTestBefore.AddDate(0, 3, 0))
	type acc struct{ mi, kwh, temp, n float64 }
	by := map[time.Month]*acc{}
	for _, d := range h.Drives {
		if d.Miles < 3 {
			continue
		}
		a := by[d.Start.Month()]
		if a == nil {
			a = &acc{}
			by[d.Start.Month()] = a
		}
		a.mi, a.kwh, a.temp, a.n = a.mi+d.Miles, a.kwh+d.EnergyKWh, a.temp+d.TempC, a.n+1
	}
	eff := func(m time.Month) float64 { return by[m].mi / by[m].kwh }
	temp := func(m time.Month) float64 { return by[m].temp / by[m].n }
	if !(eff(time.January) < eff(time.September)-0.25) {
		t.Errorf("winter %.2f mi/kWh vs early autumn %.2f: no season", eff(time.January), eff(time.September))
	}
	if !(temp(time.January) < 10 && temp(time.August) > 20) {
		t.Errorf("January %.1f C, August %.1f C", temp(time.January), temp(time.August))
	}
	var minT, maxT = 99.0, -99.0
	for _, d := range h.Drives {
		minT, maxT = math.Min(minT, d.TempC), math.Max(maxT, d.TempC)
	}
	if minT > 3 || maxT < 28 {
		t.Errorf("temperatures %.1f to %.1f C", minT, maxT)
	}
}

// A DC fast stop is a capacity sample (30 points or more, over 20 kW) and the pack figure drifts slowly down, so the capacity chart draws.
func TestSampleHistory_FastStopsCarryCapacity(t *testing.T) {
	t.Parallel()
	h := NewSampleHistory(sampleHistTestBefore)
	var firstPack, lastPack float64
	n := 0
	for _, c := range h.Charges {
		if !c.DCFC || c.EndSoC-c.StartSoC < 30 || c.MaxKW <= 20 {
			continue
		}
		pack := c.EnergyKWh / (c.EndSoC - c.StartSoC) * 100
		if pack < 120 || pack > 140 {
			t.Fatalf("pack %.1f kWh from %+v", pack, c)
		}
		if n == 0 {
			firstPack = pack
		}
		lastPack = pack
		n++
	}
	if n < 12 {
		t.Fatalf("only %d capacity samples", n)
	}
	if lastPack > firstPack+1 {
		t.Errorf("the pack grew from %.1f to %.1f kWh", firstPack, lastPack)
	}
}
