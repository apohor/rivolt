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
