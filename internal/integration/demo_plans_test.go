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
// saved trips alone.
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
	if _, err := pool.ExecContext(ctx, `INSERT INTO saved_trips (user_id, name, inputs) VALUES ($1, 'Other', '{}')`, uid); err != nil {
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

	if n, err := demo.SeedPlans(ctx, pool, sf, now); err != nil || n != 2 {
		t.Fatalf("first pass: n=%d err=%v", n, err)
	}
	first := read()
	if len(first) != 3 || first["Las Vegas weekend"].id == "" || first["Las Vegas back home"].id == "" {
		t.Fatalf("rows = %+v", first)
	}
	var plan json.RawMessage
	if err := pool.QueryRowContext(ctx, `SELECT plan FROM saved_trips WHERE user_id = $1 AND name = 'Las Vegas weekend'`, uid).Scan(&plan); err != nil || len(plan) == 0 {
		t.Fatalf("plan %s err %v", plan, err)
	}
	if n, err := demo.SeedPlans(ctx, pool, sf, now.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("same loop: n=%d err=%v, want no rewrite", n, err)
	}
	if n, err := demo.SeedPlans(ctx, pool, sf, now.Add(60*24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("next loop: n=%d err=%v", n, err)
	}
	next := read()
	for _, name := range []string{"Las Vegas weekend", "Las Vegas back home"} {
		a, b := first[name], next[name]
		if a.id != b.id || a.depart == b.depart || !b.created.After(a.created) {
			t.Errorf("%s: %+v then %+v, want the same row on a later trip", name, a, b)
		}
	}
	if next["Other"] != first["Other"] {
		t.Errorf("another saved trip changed: %+v -> %+v", first["Other"], next["Other"])
	}
}
