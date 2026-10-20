//go:build integration

package demo

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The sample weather fills every demo drive that lacks stored weather, once, with
// a plausible temperature, marked as a sample; other users' drives are untouched.
func TestApplySampleWeather(t *testing.T) {
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserFull(ctx, pool, Username, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "demo-weather-other")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		id  any
		rid string
	}{{uid, "rv-demo-wx"}, {other, "rv-other-wx"}} {
		var vid string
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,$2,'R1S') RETURNING id`, u.id, u.rid).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		for i, ts := range []string{"2026-09-10T15:00:00Z", "2026-10-10T08:00:00Z"} {
			if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, start_lat, start_lon, source)
				VALUES ($1,$2,$3,$4::timestamptz,$4::timestamptz + interval '30 minutes',10,34.16,-118.17,'seed')`, u.id, vid, u.rid+"-"+string(rune('a'+i)), ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := ApplySampleWeather(ctx, pool)
	if err != nil || n != 2 {
		t.Fatalf("first pass = %d rows (%v), want the demo's 2 drives", n, err)
	}
	if n, err := ApplySampleWeather(ctx, pool); err != nil || n != 0 {
		t.Errorf("second pass = %d rows (%v), want 0", n, err)
	}
	rows, err := pool.QueryContext(ctx, `SELECT temp_c, provider, coarse_lat FROM drive_weather WHERE user_id = $1`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := 0
	for rows.Next() {
		var temp, lat float64
		var prov string
		if err := rows.Scan(&temp, &prov, &lat); err != nil {
			t.Fatal(err)
		}
		got++
		if temp < 5 || temp > 30 || prov != "sample" || lat != 34.2 {
			t.Errorf("weather row = %v °C, provider %q, lat %v; want a plausible sample temperature at 0.1 degree", temp, prov, lat)
		}
	}
	if got != 2 {
		t.Errorf("%d weather rows for the demo user, want 2", got)
	}
	var n2 int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM drive_weather WHERE user_id = $1`, other).Scan(&n2); err != nil || n2 != 0 {
		t.Errorf("another user got %d weather rows (%v)", n2, err)
	}
}
