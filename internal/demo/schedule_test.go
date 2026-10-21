package demo

import (
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/ledger"
	"github.com/apohor/rivolt/internal/offpeak"
)

func TestDemoScheduleIsAValidUserSchedule(t *testing.T) {
	s := DemoSchedule()
	if again, e := offpeak.Normalize(s); e != nil || len(again.Windows) != 1 {
		t.Fatalf("not a valid schedule: %v", e)
	}
	if s.TZ != "America/Los_Angeles" || s.Windows[0].Rate != DemoOffpeakRate || DemoOffpeakRate >= HomeRate {
		t.Errorf("schedule = %+v (home rate %v)", s, HomeRate)
	}
}

// The savings figure the demo shows ties to a hand calculation: a weekday-evening
// plug-in at the demo home rate, moved into the example overnight window.
func TestDemoSavingsMatchAHandCalculation(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	// Wednesday 2026-10-21 18:00 to Thursday 07:00 local: three of these.
	var ss []ledger.HomeSession
	for _, day := range []int{7, 14, 21} {
		st := time.Date(2026, 10, day, 18, 0, 0, 0, la)
		ss = append(ss, ledger.HomeSession{ID: "s", Start: st, End: st.AddDate(0, 0, 1).Add(-11 * time.Hour), KWh: 30, ActiveSec: 4 * 3600, Cost: 30 * HomeRate})
	}
	got := ledger.EstimateSavings(ss, DemoSchedule(), HomeRate)
	// By hand: each session paid 30 x 0.16 = 4.80 and would cost 30 x 0.09 = 2.70
	// in the 21:00-06:00 window (it is plugged in 13 hours and needs 4): 2.10 each.
	want := 3 * 30 * (HomeRate - DemoOffpeakRate)
	if got.CouldHaveSaved == nil || math.Abs(*got.CouldHaveSaved-want) > 0.005 || got.Moved != 3 || got.MovedKWh != 90 {
		t.Fatalf("saved %v (want %.2f), moved %d, %v kWh", got.CouldHaveSaved, want, got.Moved, got.MovedKWh)
	}
}
