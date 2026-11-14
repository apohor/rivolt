//go:build integration

package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
)

// The demo vehicle's finished live charges come back as synthetic billing summaries; an open charge, a charge with no energy and
// another vehicle's charge do not.
func TestReplayChargeSummariesFromRecordedCharges(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	uid, err := db.EnsureUser(ctx, pool, "replay-receipts")
	if err != nil {
		t.Fatal(err)
	}
	store := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	demo := DemoVehicleID(uid)
	start := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	mk := func(id, veh string, off time.Duration, kwh float64, final string) charges.Charge {
		return charges.Charge{
			ID: id, VehicleID: veh, StartedAt: start.Add(off), EndedAt: start.Add(off + time.Hour),
			StartSoCPct: 20, EndSoCPct: 60, EnergyAddedKWh: kwh, ActiveSeconds: 3000, MaxPowerKW: 50,
			FinalState: final, Source: "live", Currency: "USD",
		}
	}
	for _, c := range []charges.Charge{
		mk("live_demo_c_1", demo, 0, 40, "charging_complete"),
		mk("live_demo_c_2", demo, 90*time.Minute, 0, "charging_complete"),
		mk("live_demo_c_3", demo, 150*time.Minute, 25, "charging_active"),
		mk("live_other_c_1", "rv-other", 0, 30, "charging_complete"),
	} {
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	got, err := replayChargeSummaries(pool)(ctx, demo)
	if err != nil || len(got) != 1 {
		t.Fatalf("summaries = %+v, %v; want the one 40 kWh charge", got, err)
	}
	s := got[0]
	if s.EnergyKWh == nil || *s.EnergyKWh != 40 || s.TotalCost == nil || *s.TotalCost != 14 || s.Currency != "USD" {
		t.Errorf("summary = %+v, want 40 kWh, 14.00 USD", s)
	}
	if !s.StartedAt.Equal(start) || !s.EndedAt.Equal(start.Add(time.Hour)) || s.TransactionID == "" {
		t.Errorf("window or id wrong: %+v", s)
	}
}
