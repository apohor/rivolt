package rivian

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// A drive that goes straight into charging, and a charge the car drives
// away from, are closed by the mutual-exclusion path. Those closes must
// fire the close hooks like the normal ones (drive weather, charge
// notifications); they used to only upsert.
func TestMutualExclusionClosesFireHooks(t *testing.T) {
	t.Parallel()
	r := NewReplayer("veh-1")
	var mu sync.Mutex
	var closedCharges []charges.Charge
	r.m.chargeCloseHook = func(_ context.Context, c charges.Charge) {
		mu.Lock()
		closedCharges = append(closedCharges, c)
		mu.Unlock()
	}
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	frame := func(sec int, gear, cs, status string, kw, soc, odoKm, lat float64) State {
		return State{At: t0.Add(time.Duration(sec) * time.Second), Gear: gear, ChargerState: cs, ChargerStatus: status,
			ChargerPowerKW: kw, BatteryLevelPct: soc, OdometerKm: odoKm, Latitude: lat, Longitude: -118,
			SpeedKph: map[bool]float64{true: 80, false: 0}[gear == "D"], PowerState: map[bool]string{true: "go", false: "ready"}[gear == "D"]}
	}
	var fs []State
	// Drive 20 min, then the very next frame is P and fast-charging.
	for i := 0; i <= 120; i++ {
		fs = append(fs, frame(i*10, "D", "charger_disconnected", "chrgr_sts_not_connected", 0, 80-float64(i)*0.1, 1000+float64(i)*0.2, 34+float64(i)*0.001))
	}
	for i := 1; i <= 120; i++ { // 20 min charging
		fs = append(fs, frame(1200+i*10, "P", "charging_active", "chrgr_sts_connected_charging", 150, 68+float64(i)*0.15, 1024, 34.12))
	}
	// ...and the next frame is D again, unplugged.
	for i := 1; i <= 30; i++ {
		fs = append(fs, frame(2400+i*10, "D", "charger_disconnected", "chrgr_sts_not_connected", 0, 86-float64(i)*0.1, 1024+float64(i)*0.2, 34.12+float64(i)*0.001))
	}
	r.FeedAll(fs)

	drives := r.Drives() // waits for the async hooks to settle
	if len(drives) != 1 || !drives[0].StartedAt.Equal(t0) {
		t.Fatalf("drive close hook: %d drives %+v, want the first drive (closed by charging)", len(drives), drives)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		n := len(closedCharges)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(closedCharges) != 1 || closedCharges[0].FinalState != "abandoned" || closedCharges[0].EnergyAddedKWh <= 0 {
		t.Fatalf("charge close hook: %+v, want the charge (closed by driving away)", closedCharges)
	}
}

// A session's close hook fires once however often it's closed, and the
// fired set stays bounded over a long run.
func TestCloseHooksFireOncePerSession(t *testing.T) {
	t.Parallel()
	r := NewReplayer("veh-1")
	var mu sync.Mutex
	n := 0
	r.m.chargeCloseHook = func(context.Context, charges.Charge) { mu.Lock(); n++; mu.Unlock() }
	row := charges.Charge{ID: "live_veh-1_c_1", EnergyAddedKWh: 40}
	for i := 0; i < 5; i++ {
		r.m.fireChargeCloseHook(row)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	mu.Lock()
	if n != 1 {
		t.Errorf("charge hook fired %d times for one session, want 1", n)
	}
	mu.Unlock()
	if !r.m.hookOnce("d", "x") || r.m.hookOnce("d", "x") || !r.m.hookOnce("c", "x") {
		t.Error("hookOnce: first per kind+id true, repeats false")
	}
	for i := 0; i < 5000; i++ { // a year of sessions
		r.m.hookOnce("c", fmt.Sprint("s", i))
	}
	r.m.mu.Lock()
	size := len(r.m.hooksFired)
	r.m.mu.Unlock()
	if size > hookOnceMax {
		t.Errorf("fired set grew to %d, want ≤ %d", size, hookOnceMax)
	}
}

// Fragments closed by the mutual-exclusion path don't fire hooks: a
// charge abandoned with under 1 kWh, a drive under a minute and 0.1 mi
// that ran into charging.
func TestMutualExclusionHookThresholds(t *testing.T) {
	t.Parallel()
	r := NewReplayer("veh-1")
	var mu sync.Mutex
	var drivesN, chargesN int
	r.m.chargeCloseHook = func(context.Context, charges.Charge) { mu.Lock(); chargesN++; mu.Unlock() }
	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	f := func(sec int, gear, cs, status string, kw, mph, odoKm float64) State {
		return State{At: t0.Add(time.Duration(sec) * time.Second), Gear: gear, ChargerState: cs, ChargerStatus: status,
			ChargerPowerKW: kw, SpeedKph: mph / kphToMi, BatteryLevelPct: 50, OdometerKm: odoKm, Latitude: 34, Longitude: -118,
			PowerState: "ready"}
	}
	// A 20-s drive of 0.01 mi straight into charging.
	r.Feed(f(0, "P", "charger_disconnected", "chrgr_sts_not_connected", 0, 0, 1000))
	r.Feed(f(10, "D", "charger_disconnected", "chrgr_sts_not_connected", 0, 3, 1000))
	r.Feed(f(30, "D", "charger_disconnected", "chrgr_sts_not_connected", 0, 3, 1000.016))
	r.Feed(f(31, "P", "charging_active", "chrgr_sts_connected_charging", 7, 0, 1000.016))
	// 10 s of 7 kW (0.02 kWh), then the car drives off.
	r.Feed(f(41, "P", "charging_active", "chrgr_sts_connected_charging", 7, 0, 1000.016))
	r.Feed(f(45, "D", "charger_disconnected", "chrgr_sts_not_connected", 0, 10, 1000.03))
	drivesN = len(r.Drives())
	mu.Lock()
	defer mu.Unlock()
	if drivesN != 0 || chargesN != 0 {
		t.Errorf("hooks fired for fragments: %d drives, %d charges; want none", drivesN, chargesN)
	}
}

// A plug-in that only negotiates and is unplugged two minutes later moved no
// energy: no close hook (so no "Charging complete · 0.0 kWh" push). A charge
// that did add energy still fires it.
func TestZeroEnergyChargeFiresNoCloseHook(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	run := func(kw, soc0, soc1 float64) []charges.Charge {
		r := NewReplayer("veh-1")
		var mu sync.Mutex
		var closed []charges.Charge
		r.m.chargeCloseHook = func(_ context.Context, c charges.Charge) {
			mu.Lock()
			closed = append(closed, c)
			mu.Unlock()
		}
		var fs []State
		for i := 0; i <= 12; i++ { // two minutes of frames
			fs = append(fs, State{At: t0.Add(time.Duration(i) * 10 * time.Second), Gear: "P", ChargerState: "charging_active",
				ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: kw, BatteryLevelPct: soc0 + (soc1-soc0)*float64(i)/12,
				OdometerKm: 1000, Latitude: 34, Longitude: -118, PowerState: "ready"})
		}
		fs = append(fs, State{At: t0.Add(3 * time.Minute), Gear: "P", ChargerState: "charger_disconnected",
			ChargerStatus: "chrgr_sts_not_connected", BatteryLevelPct: soc1, OdometerKm: 1000, Latitude: 34, Longitude: -118, PowerState: "ready"})
		r.FeedAll(fs)
		_ = r.Charges() // waits for the async hooks to settle
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]charges.Charge(nil), closed...)
	}
	if got := run(0, 70, 70); len(got) != 0 {
		t.Errorf("zero-energy charge fired the close hook: %+v", got)
	}
	if got := run(11, 70, 72); len(got) != 1 {
		t.Errorf("a charge that added energy fired %d hooks, want 1", len(got))
	}
}
