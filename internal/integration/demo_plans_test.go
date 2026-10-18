//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/settings"
)

// TestDemoSamplePlansSeed: the pass writes the two Las Vegas plans for
// the demo user, rewrites nothing while the loop is unchanged, moves
// the same rows (same ids) to the next loop's trip, and leaves other
// saved trips alone. The rows carry the fixed ids the presets use: an
// older row with a random id (preview before this change) moves to it.
func TestDemoSamplePlansSeed(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserFull(ctx, pool, demo.Username, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO saved_trips (user_id, name, inputs) VALUES ($1, 'Other', '{}'), ($1, 'Las Vegas weekend', '{}')`, uid); err != nil {
		t.Fatal(err)
	}
	sf := settings.NewFactory(pool)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	type row struct {
		id, depart string
		created    time.Time
	}
	read := func() map[string]row {
		t.Helper()
		rows, err := pool.QueryContext(ctx, `SELECT name, id::text, inputs->>'departure_at', created_at FROM saved_trips WHERE user_id = $1`, uid)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]row{}
		for rows.Next() {
			var name string
			var r row
			var dep *string
			if err := rows.Scan(&name, &r.id, &dep, &r.created); err != nil {
				t.Fatal(err)
			}
			if dep != nil {
				r.depart = *dep
			}
			out[name] = r
		}
		return out
	}

	// Nothing recorded yet: nothing to pair with, no rows.
	if n, err := demo.SeedPlans(ctx, pool, sf, now); err != nil || n != 0 {
		t.Fatalf("before any frame: n=%d err=%v", n, err)
	}
	// The replay has recorded since well before the trip.
	var vid string
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2) RETURNING id::text`, uid, demo.VehicleID()).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	recorded := now.AddDate(0, -3, 0)
	if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicle_state (user_id, vehicle_id, at, source) VALUES ($1, $2, $3, 'replay')`, uid, vid, recorded); err != nil {
		t.Fatal(err)
	}
	if n, err := demo.SeedPlans(ctx, pool, sf, now); err != nil || n != 3 {
		t.Fatalf("first pass: n=%d err=%v", n, err)
	}
	first := read()
	if len(first) != 4 { // the two legs, the two-day trip, and Other
		t.Fatalf("rows = %+v", first)
	}
	for name, id := range demo.SampleTripIDs() {
		if first[name].id != id.String() {
			t.Errorf("%s: id %s, want %s", name, first[name].id, id)
		}
	}
	if first[demo.MultidayTripName].id != demo.SampleMultidayTripID().String() {
		t.Errorf("two-day trip id %s, want %s", first[demo.MultidayTripName].id, demo.SampleMultidayTripID())
	}
	var plan json.RawMessage
	if err := pool.QueryRowContext(ctx, `SELECT plan FROM saved_trips WHERE user_id = $1 AND name = 'Las Vegas weekend'`, uid).Scan(&plan); err != nil || len(plan) == 0 {
		t.Fatalf("plan %s err %v", plan, err)
	}
	// A restart (or a second replica) seeds again: no change.
	if n, err := demo.SeedPlans(ctx, pool, sf, now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("same loop: n=%d err=%v, want no rewrite", n, err)
	}
	if n, err := demo.SeedPlans(ctx, pool, sf, now.Add(60*24*time.Hour)); err != nil || n != 3 {
		t.Fatalf("next loop: n=%d err=%v", n, err)
	}
	next := read()
	for _, name := range []string{"Las Vegas weekend", "Las Vegas back home", demo.MultidayTripName} {
		a, b := first[name], next[name]
		if a.id != b.id || a.depart == b.depart || !b.created.After(a.created) {
			t.Errorf("%s: %+v then %+v, want the same row on a later trip", name, a, b)
		}
	}
	if next["Other"] != first["Other"] {
		t.Errorf("another saved trip changed: %+v -> %+v", first["Other"], next["Other"])
	}
}
