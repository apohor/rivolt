//go:build integration

package charges_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/repair"
)

// repair-charge-fragments against real Postgres: finds the incident
// shape, leaves unrelated and other users' rows alone, and Apply merges
// into the keeper.
func TestRepairChargeFragments(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	alice, err := db.EnsureUser(ctx, pool, "repair-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "repair-bob")
	if err != nil {
		t.Fatal(err)
	}
	f := charges.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	t0 := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	seed := func(uid interface{ String() string }, store *charges.Store, id, veh string, startMin, durMin int, kwh float64, state string) {
		t.Helper()
		st := t0.Add(time.Duration(startMin) * time.Minute)
		c := charges.Charge{ID: id, VehicleID: veh, StartedAt: st, EndedAt: st.Add(time.Duration(durMin) * time.Minute),
			StartSoCPct: 20 + float64(startMin)/3, EndSoCPct: 25 + float64(startMin)/3, EnergyAddedKWh: kwh,
			MaxPowerKW: 100 + float64(startMin), ActiveSeconds: float64(durMin * 60), FinalState: state, Source: "live",
			Cost: kwh * 0.12, Currency: "USD", PricePerKWh: 0.12}
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	as := f.For(alice)
	// The incident: four pieces, running energy totals, last complete.
	seed(alice, as, "live_a_c_1", "rv-a", 0, 3, 5, "abandoned")
	seed(alice, as, "live_a_c_2", "rv-a", 3, 3, 15, "abandoned")
	seed(alice, as, "live_a_c_3", "rv-a", 6, 3, 30, "abandoned")
	seed(alice, as, "live_a_c_4", "rv-a", 9, 3, 41, "charging_complete")
	// A separate, genuine session hours later (must stay).
	seed(alice, as, "live_a_c_9", "rv-a", 600, 40, 20, "charging_complete")
	// Bob has the same shape; a --user alice run must not touch it.
	bs := f.For(bob)
	seed(bob, bs, "live_b_c_1", "rv-b", 0, 3, 5, "abandoned")
	seed(bob, bs, "live_b_c_2", "rv-b", 3, 3, 15, "charging_complete")

	count := func(user string) int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM charges c JOIN users u ON u.id = c.user_id WHERE u.username = $1`, user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	pieces, err := repair.Load(ctx, pool, "repair-alice", time.Time{}, time.Time{}, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	groups := repair.FindGroups(pieces, 15*time.Second)
	if len(groups) != 1 || len(groups[0].Pieces) != 4 {
		t.Fatalf("groups = %d (pieces=%d), want 1 group of 4", len(groups), len(pieces))
	}
	if count("repair-alice") != 5 || count("repair-bob") != 2 {
		t.Fatal("dry run (Load/FindGroups) must not change rows")
	}
	if err := repair.Apply(ctx, pool, groups[0]); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if count("repair-alice") != 2 || count("repair-bob") != 2 {
		t.Fatalf("after apply: alice %d (want 2), bob %d (want 2)", count("repair-alice"), count("repair-bob"))
	}
	var energy, maxPow, active float64
	var started time.Time
	if err := pool.QueryRowContext(ctx, `
		SELECT energy_added_kwh, max_power_kw, active_seconds, started_at FROM charges
		 WHERE external_id = 'live_a_c_4'`).Scan(&energy, &maxPow, &active, &started); err != nil {
		t.Fatal(err)
	}
	if energy != 41 || maxPow != 109 || active != 4*180 || !started.Equal(t0) {
		t.Fatalf("keeper = energy %v maxPow %v active %v started %v", energy, maxPow, active, started)
	}
	// Re-running is a no-op.
	pieces, _ = repair.Load(ctx, pool, "repair-alice", time.Time{}, time.Time{}, 15*time.Second)
	if g := repair.FindGroups(pieces, 15*time.Second); len(g) != 0 {
		t.Fatalf("second run found %d groups", len(g))
	}
}
