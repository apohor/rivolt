//go:build integration

// /drain SQL against real Postgres with every migration applied.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

func TestDrainStore_PairsSleepsDaysAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "drain-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "drain-bob")
	if err != nil {
		t.Fatal(err)
	}
	var va string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id, pack_kwh) VALUES ($1,'rv-drain',135) RETURNING id`, alice).Scan(&va); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-6 * time.Hour)
	row := func(at time.Time, soc float64, ps string) {
		t.Helper()
		if _, err := pool.Exec(`INSERT INTO vehicle_state
			(user_id, vehicle_id, at, source, battery_level_pct, odometer_mi, power_state, shift_state, speed_mph, charging_state)
			VALUES ($1,$2,$3,'live',$4,1000,$5,'P',0,'charger_disconnected')`,
			alice, va, at, soc, ps); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 36 samples, 10 min apart: SoC 80 then 79 (one point of drain),
	// one 30 min sleep (rows 20-22), plus a sleep that lasts 30 s.
	for i := 0; i < 36; i++ {
		at := start.Add(time.Duration(i) * 10 * time.Minute)
		soc, ps := 80.0, "ready"
		if i >= 18 {
			soc = 79
		}
		if i >= 20 && i <= 22 {
			ps = "sleep"
		}
		row(at, soc, ps)
	}
	q := start.Add(28*10*time.Minute + 2*time.Minute)
	row(q, 79, "sleep")
	row(q.Add(30*time.Second), 79, "ready")

	loc, _ := time.LoadLocation("America/Chicago")
	raw, err := newDrainStore(pool)(ctx, alice, "rv-drain", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	near := func(got, want, tol float64, what string) {
		t.Helper()
		if math.Abs(got-want) > tol {
			t.Errorf("%s = %v, want %v ± %v", what, got, want, tol)
		}
	}
	near(raw.W7.ParkH, 350.0/60, 0.01, "7d parked hours")
	near(raw.W7.DropPct, 1, 0.001, "7d drop")
	near(raw.W7.ObsH, 350.0/60, 0.01, "7d observed hours")
	near(raw.W7.AwakeH, 319.5/60, 0.01, "7d awake hours")
	if raw.W7.Sleeps != 2 || raw.W7.Quick != 1 || raw.W7.MedianSleepS == nil {
		t.Errorf("7d sleeps = %+v", raw.W7)
	}
	// The pairs split by the power state at their start: the 30 min sleep (rows 20 to
	// 22) and the 30 s one are asleep, the rest awake, and the one lost point came
	// while awake.
	near(raw.W7.AsleepPairH, 30.5/60, 0.01, "7d asleep pair hours")
	near(raw.W7.AsleepPairH+raw.W7.AwakePairH, raw.W7.ParkH, 0.001, "asleep + awake pair hours")
	near(raw.W7.AsleepDrop, 0, 0.001, "7d asleep drop")
	near(raw.W7.AwakeDrop, 1, 0.001, "7d awake drop")
	if !raw.PowerStateReliable {
		t.Error("a truck with no model is an R1: power state reliable")
	}
	if raw.W30.ParkH != raw.W7.ParkH {
		t.Errorf("30d parked %v != 7d %v for a 6 h history", raw.W30.ParkH, raw.W7.ParkH)
	}
	if raw.PackKWh == nil || *raw.PackKWh != 135 {
		t.Errorf("pack = %v", raw.PackKWh)
	}
	var daySum float64
	var daySleeps int
	for _, a := range raw.Days {
		daySum += a.ParkH
		daySleeps += a.Sleeps
	}
	near(daySum, raw.W7.ParkH, 0.001, "sum of daily parked hours")
	if daySleeps != 2 {
		t.Errorf("daily sleeps sum = %d", daySleeps)
	}

	// Another user asking for Alice's vehicle id sees nothing.
	other, err := newDrainStore(pool)(ctx, bob, "rv-drain", loc, now)
	if err != nil {
		t.Fatal(err)
	}
	if other.W7.ParkH != 0 || other.W7.Sleeps != 0 || other.PackKWh != nil || len(other.Days) != 0 {
		t.Fatalf("cross-user leak: %+v", other)
	}
}

// Rows with NULL gear and charging_state (some recorder paths write
// them) are parked-awake time, not a gap: awake hours must equal the
// observed hours for an all-awake history.
func TestDrainStore_NullGearAndChargingStateCountAsParkedAwake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "drain-null-gear")
	if err != nil {
		t.Fatal(err)
	}
	var vid string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-null-gear') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	for i := 0; i < 7; i++ {
		if _, err := pool.Exec(`INSERT INTO vehicle_state (user_id, vehicle_id, at, source, battery_level_pct, odometer_mi, power_state)
			VALUES ($1,$2,$3,'live',80,1000,'ready')`, uid, vid, now.Add(-time.Duration(60-i*10)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := newDrainStore(pool)(ctx, uid, "rv-null-gear", time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	// 7 rows 10 min apart; the one at `now` is outside the half-open
	// window, so 5 intervals = 50 min, all awake.
	const want = 50.0 / 60
	if math.Abs(raw.W7.ObsH-want) > 1e-6 {
		t.Fatalf("observed hours = %v, want %v", raw.W7.ObsH, want)
	}
	if math.Abs(raw.W7.AwakeH-raw.W7.ObsH) > 1e-9 {
		t.Fatalf("awake %v should equal observed %v for NULL gear/charging rows", raw.W7.AwakeH, raw.W7.ObsH)
	}
}

// A week of parked history in two halves: asleep with no loss, then awake losing
// ten points. The split says all of the loss came while awake.
func TestDrainStore_AsleepAwakeSplit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	u, err := db.EnsureUser(ctx, pool, "drain-split")
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := pool.QueryRow(`INSERT INTO vehicles (user_id, rivian_vehicle_id, model, pack_kwh) VALUES ($1,'rv-split','R1T',135) RETURNING id`, u).Scan(&v); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-72 * time.Hour)
	for i := 0; i < 432; i++ { // 72 h at 10 min
		soc, ps := 80.0, "sleep"
		if i >= 216 {
			ps = "ready"
			soc = 80 - float64(i-216)/21.6 // ten points over the awake half
		}
		if _, err := pool.Exec(`INSERT INTO vehicle_state
			(user_id, vehicle_id, at, source, battery_level_pct, odometer_mi, power_state, shift_state, speed_mph, charging_state)
			VALUES ($1,$2,$3,'live',$4,1000,$5,'P',0,'charger_disconnected')`,
			u, v, start.Add(time.Duration(i)*10*time.Minute), math.Round(soc), ps); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	raw, err := newDrainStore(pool)(ctx, u, "rv-split", time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buildDrain(raw, now, time.UTC).Windows["7d"].Split
	if !got.Sufficient || got.AwakeShareOfLoss == nil || *got.AwakeShareOfLoss != 1 {
		t.Fatalf("split = %+v, want sufficient with all of the loss awake", got)
	}
	if math.Abs(got.AsleepHours-36) > 0.5 || math.Abs(got.AwakeHours-36) > 0.5 || got.AsleepPointsLost == nil || *got.AsleepPointsLost != 0 || math.Abs(*got.AwakePointsLost-10) > 1 {
		t.Errorf("split = %+v, want 36 h asleep losing 0, 36 h awake losing about 10", got)
	}
	// The same history on an R2 now states the split too: its power state is
	// reported (rivian.FieldSupport), since the fill reached 98.5% on Oct 19. The
	// unreliable path is covered by TestBuildDrainSplit.
	if _, err := pool.Exec(`UPDATE vehicles SET model = 'R2' WHERE id = $1`, v); err != nil {
		t.Fatal(err)
	}
	raw, err = newDrainStore(pool)(ctx, u, "rv-split", time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if sp := buildDrain(raw, now, time.UTC).Windows["7d"].Split; !sp.Sufficient || sp.AwakeShareOfLoss == nil || *sp.AwakeShareOfLoss != 1 {
		t.Errorf("R2 split = %+v, want the same split as an R1", sp)
	}
}
