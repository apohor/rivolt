//go:build integration

package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// An open charge row is resumed only across a restart-sized gap. Rivian's session total counts per plug-in and restarts at a
// re-plug, so a row last written hours earlier is a different charge: resuming it replaced its energy with the new plug-in's
// total (the first part lost) and glued the two charges and the drive between them into one row.
func TestResumeOpenChargeOnlyAcrossARestartGap(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "resume-gap")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	m := NewStateMonitor(nil, nil)
	m.SetStores(nil, nil, store)

	last := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second) // the row's last write
	if err := store.Upsert(ctx, charges.Charge{ID: "live_v-rg_c_1", VehicleID: "v-rg", StartedAt: last.Add(-5 * time.Hour), EndedAt: last,
		StartSoCPct: 40, EndSoCPct: 70, EnergyAddedKWh: 30, MaxPowerKW: 7, FinalState: "charging_active", Source: "live"}); err != nil {
		t.Fatal(err)
	}

	// A new plug-in hours later: not this charge.
	if c := m.resumeOpenCharge(ctx, &State{VehicleID: "v-rg", At: last.Add(2 * time.Hour)}); c != nil {
		t.Fatalf("a row last written 2 h before the first frame was resumed (energy %.1f kWh)", c.energyIntKWh)
	}
	// A restart or a handoff: seconds to minutes. Resumed, with its energy kept.
	c := m.resumeOpenCharge(ctx, &State{VehicleID: "v-rg", At: last.Add(2 * time.Minute)})
	if c == nil {
		t.Fatal("a row last written 2 minutes before the first frame was not resumed")
	}
	if c.energyIntKWh != 30 || c.id != "live_v-rg_c_1" {
		t.Errorf("resumed %q with %.1f kWh, want the row's 30 kWh", c.id, c.energyIntKWh)
	}
	// Just over the stale gap: not resumed.
	if c := m.resumeOpenCharge(ctx, &State{VehicleID: "v-rg", At: last.Add(liveChargeMaxGap + time.Minute)}); c != nil {
		t.Errorf("a row older than the stale gap was resumed")
	}
}
