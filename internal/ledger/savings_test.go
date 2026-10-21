package ledger

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/offpeak"
)

func sched(t *testing.T, ws ...offpeak.Window) offpeak.Schedule {
	t.Helper()
	s, e := offpeak.Normalize(offpeak.Schedule{TZ: "America/Chicago", Windows: ws})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

var chi, _ = time.LoadLocation("America/Chicago")

func local(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, chi) }

// night is Mon-Fri 21:00-06:00 at 9 cents; everything else is the 21-cent home rate.
func nightSched(t *testing.T) offpeak.Schedule {
	return sched(t, offpeak.Window{Days: offpeak.Weekdays, Start: "21:00", End: "06:00", Rate: 0.09})
}

func TestEstimateSavingsMovesToTheCheapestWindowThatFits(t *testing.T) {
	// 2026-10-21 is a Wednesday. Plugged in 18:00 for 14 h, 30 kWh over 4 h of
	// charging, paid at the flat 0.21 = 6.30. The night window holds 4 h: 30 kWh
	// at 0.09 = 2.70, a saving of 3.60.
	s := HomeSession{ID: "a", Start: local(21, 18, 0), End: local(22, 8, 0), KWh: 30, ActiveSec: 4 * 3600, Cost: 6.30}
	// Plugged in 18:00-21:00 for a 3 h charge: only the flat-rate hours are reachable,
	// so it already paid the cheapest it could.
	brief := HomeSession{ID: "b", Start: local(22, 18, 0), End: local(22, 21, 0), KWh: 20, ActiveSec: 3 * 3600, Cost: 4.20}
	// Plugged in only inside the cheap hours and already paid 0.09.
	cheap := HomeSession{ID: "c", Start: local(22, 22, 0), End: local(23, 5, 0), KWh: 20, ActiveSec: 3 * 3600, Cost: 1.80}
	// Plugged in 20:00-23:00 but needs 4 h: nothing holds it.
	toolong := HomeSession{ID: "d", Start: local(22, 20, 0), End: local(22, 23, 0), KWh: 28, ActiveSec: 4 * 3600, Cost: 5.88}
	got := EstimateSavings([]HomeSession{s, brief, cheap, toolong}, nightSched(t), 0.21)
	if got.Considered != 4 || got.Moved != 1 || got.LeftOut != 3 {
		t.Fatalf("counts: %+v", got)
	}
	if got.Reasons[ReasonTooBrief] != 1 || got.Reasons[ReasonNoCheaperHours] != 2 {
		t.Errorf("reasons: %v", got.Reasons)
	}
	if got.AsChargedTotal != 18.18 || got.IfMovedTotal != 14.58 {
		t.Errorf("totals: charged %v, if moved %v", got.AsChargedTotal, got.IfMovedTotal)
	}
	if got.CouldHaveSaved == nil || *got.CouldHaveSaved != 3.60 || got.Needs != "" {
		t.Errorf("saving: %v needs %q", got.CouldHaveSaved, got.Needs)
	}
}

func TestEstimateSavingsNeverNegativeAndNeverFromCostlierHours(t *testing.T) {
	// The only window is dearer than the flat rate: nothing is cheaper, nothing saved.
	dear := sched(t, offpeak.Window{Days: offpeak.All, Start: "17:00", End: "21:00", Rate: 0.40})
	var ss []HomeSession
	for d := 21; d < 24; d++ {
		ss = append(ss, HomeSession{ID: "x", Start: local(d, 12, 0), End: local(d+1, 12, 0), KWh: 20, ActiveSec: 3 * 3600, Cost: 4.20})
	}
	got := EstimateSavings(ss, dear, 0.21)
	if got.Moved != 0 || got.CouldHaveSaved == nil || *got.CouldHaveSaved != 0 || got.IfMovedTotal != got.AsChargedTotal {
		t.Errorf("%+v", got)
	}
}

func TestEstimateSavingsNeeds(t *testing.T) {
	one := []HomeSession{{ID: "a", Start: local(21, 18, 0), End: local(22, 8, 0), KWh: 30, ActiveSec: 4 * 3600, Cost: 6.30}}
	three := append(append(one, one...), one...)
	if g := EstimateSavings(three, nightSched(t), 0); g.Needs != "home_rate" || g.CouldHaveSaved != nil {
		t.Errorf("no home rate: %+v", g)
	}
	if g := EstimateSavings(three, offpeak.Schedule{}, 0.21); g.Needs != "offpeak_schedule" || g.CouldHaveSaved != nil {
		t.Errorf("no schedule: %+v", g)
	}
	if g := EstimateSavings(one, nightSched(t), 0.21); g.Needs != "more_home_sessions" || g.CouldHaveSaved != nil || g.Considered != 1 {
		t.Errorf("one session: %+v", g)
	}
	if g := EstimateSavings(three, nightSched(t), 0.21); g.Needs != "" || g.CouldHaveSaved == nil || *g.CouldHaveSaved != 10.80 {
		t.Errorf("three sessions: %+v", g)
	}
}

func TestEstimateSavingsPowerFallbacks(t *testing.T) {
	// No active time: kWh over the recorded peak (30 kWh at 7.5 kW = 4 h, fits the
	// 9 h night); with neither, 7 kW is assumed (30 kWh = 4.3 h, still fits).
	base := HomeSession{Start: local(21, 18, 0), End: local(22, 8, 0), KWh: 30, Cost: 6.30}
	a, b := base, base
	a.PeakKW = 7.5
	for _, s := range []HomeSession{a, b} {
		if m, r := cheapestMove(s, nightSched(t), 0.21); r != "" || m < 2.69 || m > 2.71 {
			t.Errorf("%+v: %v %q", s, m, r)
		}
	}
	// An active time longer than the plug-in (a glitch) is capped at the span.
	g := base
	g.ActiveSec = 40 * 3600
	if _, r := cheapestMove(g, nightSched(t), 0.21); r != ReasonTooBrief {
		t.Errorf("glitchy active time = %q, want too brief (no stretch holds the whole plug-in span)", r)
	}
}

// The estimate's as-charged total is the Money statement's home AC cost.
func TestHomeACSessionsTieToTheMoneyLine(t *testing.T) {
	cfg := Config{Currency: "USD", HomeRate: 0.11, Home: home}
	mk := func(id, start string, dur time.Duration, kwh, peak float64) Charge {
		return priced(charge(id, start, dur, kwh, peak), 0.11)
	}
	cs := []Charge{
		mk("h1", "2026-10-05T02:00:00Z", 6*time.Hour, 20, 7),
		mk("h2", "2026-10-12T02:00:00Z", 6*time.Hour, 25, 7),
		at(mk("away", "2026-10-14T02:00:00Z", 2*time.Hour, 20, 7), awayLat, awayLon),
		mk("dc", "2026-10-15T02:00:00Z", 40*time.Minute, 40, 150),
		charge("unpriced", "2026-10-17T02:00:00Z", 4*time.Hour, 10, 7),
		mk("nextmonth", "2026-11-02T02:00:00Z", 4*time.Hour, 10, 7),
	}
	for i := range cs {
		if cs[i].Cost != nil {
			cs[i].CostSource = SourceHomeRate
		}
	}
	p, err := ParsePeriod("2026-10", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Charges: cs, Config: cfg, Now: ts("2026-11-15T00:00:00Z")}
	m := Build(in, p, false)
	sessions := HomeACSessions(in, p)
	var sum float64
	for _, s := range sessions {
		sum += s.Cost
	}
	line := m.Energy.ByPlaceKind["home"]["ac"]
	if len(sessions) != 2 || round2(sum) != round2(line.Cost) {
		t.Errorf("%d sessions summing %v, Money's home AC line is %v over %d sessions", len(sessions), sum, line.Cost, line.Sessions)
	}
}
