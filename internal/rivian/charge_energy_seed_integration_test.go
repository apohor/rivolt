//go:build integration

package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// A snapshot an older pod wrote carries no energy; the open row the recorder has been upserting does. Rehydrating from
// the snapshot lifts the charge to the row's energy and active time, and never lowers a snapshot's own.
func TestRehydratedChargeSeedsEnergyFromItsOpenRow(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "seed-energy")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	row := charges.Charge{
		ID: "live_rv-seed_c_1", VehicleID: "rv-seed", StartedAt: start, EndedAt: start.Add(2 * time.Hour),
		StartSoCPct: 20, EndSoCPct: 40, EnergyAddedKWh: 12.3, ActiveSeconds: 7000, MaxPowerKW: 7.5,
		FinalState: "charging_active", Source: "live", Currency: "USD",
	}
	if err := store.Upsert(ctx, row); err != nil {
		t.Fatal(err)
	}

	snapshot := func(energy float64) *StateMonitor {
		mem := newMemLiveStateStore()
		s := &liveSessions{charge: &liveCharge{id: row.ID, startedAt: start, endAt: start.Add(2 * time.Hour), startSoC: 20, endSoC: 40,
			energyIntKWh: energy, activeSeconds: 7100}}
		_ = mem.Save(ctx, "rv-seed", s.snapshot(), time.Hour)
		m := NewStateMonitor(nil, nil)
		m.SetLiveStateStore(mem)
		m.SetStores(nil, nil, store)
		return m
	}
	curr := &State{VehicleID: "rv-seed", At: start.Add(2*time.Hour + time.Minute)}

	// An older snapshot (no energy): the row's 12.3 kWh stands, and so does the larger active time.
	m := snapshot(0)
	got := m.maybeRehydrate("rv-seed", curr).charge
	if got == nil || got.energyIntKWh != 12.3 || got.progressKWh != 12.3 || got.activeSeconds != 7100 {
		t.Errorf("seeded charge = %+v, want 12.3 kWh and the snapshot's 7100 s", got)
	}
	// A snapshot with more than the row keeps its own.
	m = snapshot(15)
	if got := m.maybeRehydrate("rv-seed", curr).charge; got == nil || got.energyIntKWh != 15 {
		t.Errorf("a snapshot's own 15 kWh was lowered: %+v", got)
	}
}
