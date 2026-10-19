//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminhealth"
	"github.com/apohor/rivolt/internal/db"
)

// The admin engagement section counts who was seen, who is connected with no
// data, who reached their first drive or charge (derived from drives and
// charges, nothing written), and the mail sent.
func TestFirstDataAndEngagement(t *testing.T) {
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

	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source)
	      VALUES ($1,$2,'d1', now() - interval '50 minutes', now() - interval '40 minutes', 5, 'live')`, driver, vid)
	// A charge long ago for the lurker: first data, but outside the 28-day window.
	lvid := vehicle(lurker, "rv-3")
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source)
	      VALUES ($1,$2,'c1', now() - interval '90 days', now() - interval '90 days' + interval '1 hour', 'live')`, lurker, lvid)

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
		t.Errorf("first data %d/%d, want 1/1 (driver; the lurker's was 90 days ago)", sum.FirstData7d, sum.FirstData28d)
	}
	if sum.MedianSecondsToFirstData == nil || *sum.MedianSecondsToFirstData < 1100 || *sum.MedianSecondsToFirstData > 1300 {
		t.Errorf("median seconds to first data = %v, want about 1200 (vehicle 1 h ago, drive ended 40 min ago: 20 min)", sum.MedianSecondsToFirstData)
	}
	if sum.EmailsSent["setup_first"] != 1 || sum.EmailsSent["setup_followup"] != 1 {
		t.Errorf("emails %v", sum.EmailsSent)
	}
}

// The funnel by signup week counts, per cohort: signups, a real vehicle, a closed
// drive or charge, and a return at least a day after signup. It leaves out
// disabled accounts and the demo, counts an ElectraFi-only account as signed up but
// not connected, and does not count a session inside the first day as a return.
func TestFunnelByWeek(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string, signedUp time.Time) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE users SET created_at = $2 WHERE id = $1`, uid, signedUp)
		return uid
	}
	vehicle := func(uid uuid.UUID, rid string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,$2) RETURNING id`, uid, rid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	session := func(uid uuid.UUID, seen time.Time) {
		exec(`INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at, user_agent, ip_address)
		      VALUES ($1,$2,$3,$4,$5, now() + interval '1 day', 'ua', '203.0.113.9')`, uuid.New(), uid, []byte(uuid.NewString()), seen.Add(-time.Hour), seen)
	}
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	week := func(t time.Time) string {
		d := t.UTC().Truncate(24 * time.Hour)
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)).Format("2006-01-02")
	}

	full := mk("fun-full", old) // connected, first data, came back on day 3
	vid := vehicle(full, "rv-fun-1")
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source)
	      VALUES ($1,$2,'fd1', $3, $4, 5, 'live')`, full, vid, old.Add(26*time.Hour), old.Add(27*time.Hour))
	session(full, old.Add(3*24*time.Hour))
	vehicleOnly := mk("fun-veh", old) // connected, nothing else, never came back
	vehicle(vehicleOnly, "rv-fun-2")
	session(vehicleOnly, old.Add(2*time.Hour)) // inside the first day: not a return
	mk("fun-none", old)                        // signed up only
	efi := mk("fun-efi", old)                  // ElectraFi import only: not connected
	vehicle(efi, "electrafi-abc")
	fresh := mk("fun-fresh", now.Add(-2*time.Hour)) // too new to have returned
	vehicle(fresh, "rv-fun-3")
	off := mk("fun-off", old)
	exec(`UPDATE users SET disabled = TRUE WHERE id = $1`, off)
	mk(db.DemoUsername, old)

	sum := adminhealth.New(adminhealth.Config{DB: pool}).Summary(ctx).Engagement
	if sum.Status != adminhealth.OK {
		t.Fatalf("engagement %+v", sum)
	}
	got := map[string]adminhealth.FunnelWeek{}
	for _, w := range sum.FunnelWeeks {
		got[w.Week] = w
	}
	// Old cohort: full, vehicleOnly, none, efi. The disabled account and the demo
	// are left out.
	if o := got[week(old)]; o != (adminhealth.FunnelWeek{Week: week(old), Signups: 4, Connected: 2, FirstData: 1, Day2Eligible: 4, ReturnedDay2: 1}) {
		t.Errorf("old week = %+v, want 4 signups, 2 connected, 1 with data, 4 eligible, 1 returned", o)
	}
	// This week: the fresh account, connected, too new to have returned.
	if f := got[week(now)]; f != (adminhealth.FunnelWeek{Week: week(now), Signups: 1, Connected: 1}) {
		t.Errorf("this week = %+v, want 1 signup, connected, not yet eligible", f)
	}
	if len(sum.FunnelWeeks) > adminhealth.FunnelWeeks {
		t.Errorf("%d weeks, want at most %d", len(sum.FunnelWeeks), adminhealth.FunnelWeeks)
	}
}
