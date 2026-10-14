package rivian

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
)

func TestRejectedRetryDue(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if rejectedRetryDue(at, at.Add(time.Minute), false) {
		t.Error("retry 1 min after a rejection")
	}
	if !rejectedRetryDue(at, at.Add(rejectedRetryEvery), false) {
		t.Error("no retry after rejectedRetryEvery")
	}
	if !rejectedRetryDue(at, at.Add(time.Second), true) {
		t.Error("the close write must always retry")
	}
}

// The tail written after another recorder's row: starts where that row
// ended, takes its end readings as the start, and doesn't re-count the
// distance / energy / cost the other row already holds.
func TestTrimRows(t *testing.T) {
	start := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	cut := start.Add(20 * time.Minute)

	d := drives.Drive{StartedAt: start, StartSoCPct: 80, EndSoCPct: 60, StartOdometerMi: 1000, EndOdometerMi: 1050,
		DistanceMi: 50, EnergyUsedKWh: 26, StartLat: 30.27, StartLon: -97.74}
	trimDrive(&d, &sessionTrim{start: cut, startSoC: 70, startOdoMi: 1020}, 130)
	if !d.StartedAt.Equal(cut) || d.StartSoCPct != 70 || d.StartOdometerMi != 1020 || d.DistanceMi != 30 ||
		math.Abs(d.EnergyUsedKWh-13) > 1e-9 || d.StartLat != 0 {
		t.Errorf("trimmed drive = %+v", d)
	}

	c := charges.Charge{StartedAt: start, StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 60, Cost: 6.6}
	trimCharge(&c, &sessionTrim{start: cut, startSoC: 40, energyKWh: 20})
	if !c.StartedAt.Equal(cut) || c.StartSoCPct != 40 || c.EnergyAddedKWh != 40 || math.Abs(c.Cost-4.4) > 1e-9 {
		t.Errorf("trimmed charge = %+v", c)
	}
	// devils-advocate's case: the other recorder started earlier and its
	// own 30 kWh includes energy from before ours; the tail is the SoC
	// share of ours (80−40)/(80−20) × 45 = 30, not 45 − 30 = 15.
	ghostEarlier := charges.Charge{StartedAt: start, StartSoCPct: 20, EndSoCPct: 80, EnergyAddedKWh: 45, Cost: 4.5}
	trimCharge(&ghostEarlier, &sessionTrim{start: cut, startSoC: 40, energyKWh: 30})
	if math.Abs(ghostEarlier.EnergyAddedKWh-30) > 1e-9 || math.Abs(ghostEarlier.Cost-3) > 1e-9 {
		t.Errorf("SoC-scaled tail = %+v, want 30 kWh / $3", ghostEarlier)
	}
	// No SoC: fall back to subtracting the other row's energy.
	noSoC := charges.Charge{StartedAt: start, EnergyAddedKWh: 45}
	trimCharge(&noSoC, &sessionTrim{start: cut, energyKWh: 30})
	if noSoC.EnergyAddedKWh != 15 {
		t.Errorf("no-SoC fallback = %v, want 15", noSoC.EnergyAddedKWh)
	}
	over := charges.Charge{StartedAt: start, EnergyAddedKWh: 10, Cost: 1.1}
	trimCharge(&over, &sessionTrim{start: cut, energyKWh: 25})
	if over.EnergyAddedKWh != 0 || over.Cost != 0 {
		t.Errorf("energy can't go negative: %+v", over)
	}
	untouched := charges.Charge{StartedAt: start, EnergyAddedKWh: 10}
	trimCharge(&untouched, nil)
	if !untouched.StartedAt.Equal(start) || untouched.EnergyAddedKWh != 10 {
		t.Errorf("nil trim changed the row: %+v", untouched)
	}
}

// A charge still rejected at close belongs to another recorder: no
// "charging done" push from this one. An accepted one still notifies.
func TestChargeCloseHook_SkippedWhenRejected(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		m := NewStateMonitor(nil, nil)
		var fired atomic.Int32
		m.SetChargeCloseHook(func(context.Context, charges.Charge) { fired.Add(1) })
		s := &liveSessions{}
		ctx := context.Background()
		t0 := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
		s.handleChargeLifecycle(chargingFrame(t0, 40), nil, m, ctx)
		s.handleChargeLifecycle(chargingFrame(t0.Add(10*time.Minute), 45), nil, m, ctx)
		s.charge.rejected = rejected
		done := chargingFrame(t0.Add(20*time.Minute), 50)
		done.ChargerState = "charging_complete"
		s.handleChargeLifecycle(done, nil, m, ctx)
		time.Sleep(50 * time.Millisecond) // the hook runs in a goroutine
		if want := map[bool]int32{true: 0, false: 1}[rejected]; fired.Load() != want {
			t.Errorf("rejected=%v: close hook fired %d times, want %d", rejected, fired.Load(), want)
		}
	}
}
