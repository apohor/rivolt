//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminhealth"
	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/db"
)

// first_data is emitted once, for a new account's first drive; the admin
// engagement section counts who was seen, who is connected with no data, and
// the mail sent.
func TestFirstDataAndEngagement(t *testing.T) {
	t.Setenv("RIVOLT_EVENTS", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	vehicle := func(uid uuid.UUID, rid string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, created_at) VALUES ($1,$2, now() - interval '1 hour') RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	session := func(uid uuid.UUID, ago string) {
		exec(`INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip_address)
		      VALUES ($1,$2,$3, now() - interval '60 days', now() - $4::interval, now() + interval '1 day', 'ua', '203.0.113.9')`,
			uuid.New(), uid, []byte(uid.String()+ago), ago)
	}

	driver, idle, lurker := mk("driver"), mk("idle"), mk("lurker")
	vid := vehicle(driver, "rv-1")
	vehicle(idle, "rv-2")
	session(driver, "1 day")
	session(lurker, "20 days")
	session(idle, "100 days")

	// No drive yet: nothing to emit for.
	api.EmitFirstData(ctx, pool, idle)
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source)
	      VALUES ($1,$2,'d1', now() - interval '50 minutes', now() - interval '40 minutes', 5, 'live')`, driver, vid)
	api.EmitFirstData(ctx, pool, driver)
	api.EmitFirstData(ctx, pool, driver) // once only

	var n int
	var secs float64
	if err := pool.QueryRowContext(ctx, `SELECT count(*), COALESCE(max((props->>'seconds_since_connect')::float),0) FROM events WHERE name='first_data' AND user_id=$1`, driver).Scan(&n, &secs); err != nil {
		t.Fatal(err)
	}
	if n != 1 || secs < 3500 || secs > 3700 {
		t.Errorf("first_data rows %d, seconds_since_connect %v (want 1 and about 3600)", n, secs)
	}

	exec(`UPDATE users SET setup_nudge_count = 2 WHERE id = $1`, idle)
	sum := adminhealth.New(adminhealth.Config{DB: pool}).Summary(ctx).Engagement
	if sum.Status != adminhealth.OK {
		t.Fatalf("engagement %+v", sum)
	}
	if sum.Active7d != 1 || sum.Active28d != 2 {
		t.Errorf("active 7d/28d = %d/%d, want 1/2", sum.Active7d, sum.Active28d)
	}
	if sum.ConnectedNoData != 1 {
		t.Errorf("connected with no data = %d, want 1 (idle)", sum.ConnectedNoData)
	}
	if sum.FirstData7d != 1 || sum.FirstData28d != 1 {
		t.Errorf("first_data %d/%d", sum.FirstData7d, sum.FirstData28d)
	}
	if sum.EmailsSent["setup_first"] != 1 || sum.EmailsSent["setup_followup"] != 1 {
		t.Errorf("emails %v", sum.EmailsSent)
	}
}
