package rivian

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Single-owner M3-6: a monitor starts from the vehicle info stored in Postgres (no Rivian call), so the owner records its first frame with the
// truck's own pack size, and a pod that only reads has the cache too.

func TestOwnerStartsWithTheStoredPackSizeAndNoRivianCall(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL)
	if err := c.Login(context.Background(), Credentials{Email: "a@b.c", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	m := NewStateMonitor(c, nil)
	var loads atomic.Int32
	m.SetVehicleInfoLoader(func(context.Context) ([]Vehicle, error) {
		loads.Add(1)
		return []Vehicle{{ID: "veh-a", Model: "R2", PackKWh: 91.6, TrimName: "Launch", Make: "Rivian"}}, nil
	})
	before := len(g.capturedReqs)
	if got := m.PackKWhFor("veh-a"); got != 91.6 {
		t.Fatalf("pack size before the first frame = %v, want the stored 91.6 (DefaultPackKWh is %v)", got, DefaultPackKWh)
	}
	if n := len(g.capturedReqs) - before; n != 0 {
		t.Errorf("%d Rivian requests to hydrate, want 0", n)
	}
	// Once per monitor, however often it is asked.
	for i := 0; i < 3; i++ {
		_ = m.VehicleInfo("veh-a")
		_ = m.AllVehicleInfo()
	}
	if loads.Load() != 1 {
		t.Errorf("%d loads, want 1", loads.Load())
	}
	if v := m.VehicleInfo("veh-a"); v == nil || v.TrimName != "Launch" || v.Make != "Rivian" {
		t.Errorf("stored trim name and make not in the cache: %+v", v)
	}
}

// What the cache already holds (a live observation of the pack, a Rivian refresh) is never overwritten by the stored copy; only gaps are filled.
func TestHydrateFillsGapsAndKeepsWhatIsLive(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	m.observeBatteryCapacity("veh-a", 90.2) // live: the truck's reported capacity
	m.SetVehicleInfoLoader(func(context.Context) ([]Vehicle, error) {
		return []Vehicle{{ID: "veh-a", Model: "R1T", PackKWh: 131, ImageURL: "https://img.test/a.png"}, {ID: "veh-b", PackKWh: 100}}, nil
	})
	if got := m.PackKWhFor("veh-a"); got != 90.2 {
		t.Errorf("pack = %v, want the live 90.2 kept", got)
	}
	if v := m.VehicleInfo("veh-a"); v == nil || v.ImageURL != "https://img.test/a.png" || v.Model != "R1T" {
		t.Errorf("gaps not filled: %+v", v)
	}
	if got := m.PackKWhFor("veh-b"); got != 100 {
		t.Errorf("a vehicle the cache did not know: %v", got)
	}
}

// A takeover (a pod that takes a lease later) hydrates the same way, before it records a frame.
func TestTakeoverHydratesBeforeTheFirstFrame(t *testing.T) {
	a := NewStateMonitor(nil, nil)
	b := NewStateMonitor(nil, nil)
	load := func(context.Context) ([]Vehicle, error) { return []Vehicle{{ID: "veh-a", PackKWh: 91.6}}, nil }
	a.SetVehicleInfoLoader(load)
	b.SetVehicleInfoLoader(load)
	if a.PackKWhFor("veh-a") != 91.6 || b.PackKWhFor("veh-a") != 91.6 {
		t.Errorf("owner %v, taker %v; both want the stored 91.6", a.PackKWhFor("veh-a"), b.PackKWhFor("veh-a"))
	}
}

// A failed read is not marked done: the default stands for now, and the store is asked again after the retry delay, not on every call.
func TestHydrateRetriesAfterAFailure(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	m := NewStateMonitor(nil, nil)
	m.now = func() time.Time { return now }
	var calls atomic.Int32
	fail := true
	m.SetVehicleInfoLoader(func(context.Context) ([]Vehicle, error) {
		calls.Add(1)
		if fail {
			return nil, errors.New("db down")
		}
		return []Vehicle{{ID: "veh-a", PackKWh: 91.6}}, nil
	})
	for i := 0; i < 5; i++ {
		if got := m.PackKWhFor("veh-a"); got != DefaultPackKWh {
			t.Fatalf("pack = %v while the store is down, want the default", got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d reads inside the retry delay, want 1", calls.Load())
	}
	fail = false
	now = now.Add(vehicleInfoHydrateRetry + time.Second)
	if got := m.PackKWhFor("veh-a"); got != 91.6 || calls.Load() != 2 {
		t.Errorf("after the delay: pack %v, %d reads; want 91.6 and 2", got, calls.Load())
	}
}

// A monitor with no loader (source-fed replay/demo, tests) behaves as before.
func TestNoLoaderChangesNothing(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	if got := m.PackKWhFor("veh-a"); got != DefaultPackKWh {
		t.Errorf("pack = %v, want the default", got)
	}
	if len(m.AllVehicleInfo()) != 0 {
		t.Error("a monitor with no loader invented vehicles")
	}
}

// The first charge a freshly started owner records is estimated from the SoC gain with the stored pack size, not DefaultPackKWh.
func TestFirstChargeUsesTheStoredPackSize(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	m := NewStateMonitor(nil, nil)
	m.now = func() time.Time { return at.Add(30 * time.Minute) }
	m.SetVehicleInfoLoader(func(context.Context) ([]Vehicle, error) {
		return []Vehicle{{ID: chargeTestVehicle, Model: "R2", PackKWh: 91.6}}, nil
	})
	m.hydrateVehicleInfo(ctx) // what run() does before the first frame
	for minute := 0; minute <= 30; minute++ {
		m.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: at.Add(time.Duration(minute) * time.Minute),
			Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", BatteryLevelPct: 20 + float64(minute)/3, OdometerKm: 30000})
	}
	_, now := m.LiveSessionForCharge(chargeTestVehicle)
	want := 10.0 / 100 * 91.6
	if now.EnergyKWh < want-0.2 || now.EnergyKWh > want+0.2 {
		t.Errorf("energy = %.2f kWh, want about %.2f (10 points of the stored 91.6 kWh pack; the default %v would give %.2f)", now.EnergyKWh, want, DefaultPackKWh, 10.0/100*DefaultPackKWh)
	}
}
