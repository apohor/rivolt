//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/plugreminder"
)

// The reminder's Postgres reads: who is a candidate (opted in on a device, zone
// stored, not the demo account), each truck's latest state, and the two-hour
// plugged-in / charged-at-home suppression.
func TestPlugReminderSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string) uuid.UUID {
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
	optedIn, noZone, noDevice, demoish := mk("optin"), mk("nozone"), mk("nodevice"), mk("demoish")
	for _, u := range []uuid.UUID{optedIn, noZone, demoish} {
		exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, on_plug_in_reminder) VALUES ($1,$2,'k','a', TRUE)`, u, "https://push.example/"+u.String())
	}
	exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1,'https://push.example/off','k','a')`, noDevice) // default: off
	exec(`INSERT INTO engagement_prefs (user_id, timezone, plug_threshold_pct, plug_remind_at) VALUES ($1,'America/Chicago',35,'21:30')`, optedIn)
	exec(`INSERT INTO engagement_prefs (user_id) VALUES ($1)`, noZone)
	exec(`INSERT INTO engagement_prefs (user_id, timezone) VALUES ($1,'UTC')`, noDevice)
	exec(`INSERT INTO engagement_prefs (user_id, timezone) VALUES ($1,'UTC')`, demoish)

	home := func(context.Context, uuid.UUID) (float64, float64, bool, error) { return 34.1613, -118.1676, true, nil }
	src := plugreminder.NewSource(pool, home, demoish)
	cands, err := src.Candidates(ctx)
	if err != nil || len(cands) != 1 || cands[0].User != optedIn || cands[0].Timezone != "America/Chicago" || cands[0].ThresholdPct != 35 || cands[0].RemindAtMin != 21*60+30 {
		t.Fatalf("candidates %+v (%v): want only the opted-in account with a zone", cands, err)
	}

	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-plug') RETURNING id`, optedIn).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	row := func(ago time.Duration, soc float64, gear, status, state string) {
		exec(`INSERT INTO vehicle_state (vehicle_id, at, user_id, battery_level_pct, lat, lon, speed_mph, shift_state, charger_status, charging_state, source)
		      VALUES ($1,$2,$3,$4,34.1613,-118.1676,0,$5,$6,$7,'live')`, vid, now.Add(-ago), optedIn, soc, gear, status, state)
	}
	row(3*time.Hour, 30, "P", "chrgr_sts_not_connected", "charger_disconnected")
	row(1*time.Minute, 28, "P", "chrgr_sts_not_connected", "charger_disconnected")
	snaps, err := src.Latest(ctx, optedIn, now.Add(-15*time.Minute))
	if err != nil || len(snaps) != 1 || snaps[0].Vehicle != "rv-plug" || snaps[0].SoCPct != 28 || snaps[0].Plugged || snaps[0].Gear != "P" {
		t.Fatalf("latest %+v (%v)", snaps, err)
	}
	if old, _ := src.Latest(ctx, optedIn, now.Add(time.Minute)); len(old) != 0 {
		t.Errorf("a state older than the cutoff was returned: %+v", old)
	}

	// Not plugged in the last two hours: nothing suppresses.
	if p, err := src.RecentlyPlugged(ctx, optedIn, 34.1613, -118.1676, now.Add(-2*time.Hour)); err != nil || p {
		t.Errorf("suppressed with no plug-in: %v %v", p, err)
	}
	// Plugged an hour ago (a connected status) suppresses; so does a charging_* state.
	row(1*time.Hour, 25, "P", "chrgr_sts_connected_no_chrg", "charging_ready")
	if p, _ := src.RecentlyPlugged(ctx, optedIn, 34.1613, -118.1676, now.Add(-2*time.Hour)); !p {
		t.Error("a plug-in an hour ago did not suppress")
	}
	if p, _ := src.RecentlyPlugged(ctx, optedIn, 34.1613, -118.1676, now.Add(-30*time.Minute)); p {
		t.Error("a plug-in outside the window suppressed")
	}
	// A charge that started at home in the window suppresses even with no plugged sample.
	exec(`DELETE FROM vehicle_state WHERE vehicle_id = $1 AND charger_status LIKE 'chrgr_sts_connected%'`, vid)
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, source, lat, lon)
	      VALUES ($1,$2,'c1',$3,$4,'live',34.1614,-118.1675)`, optedIn, vid, now.Add(-90*time.Minute), now.Add(-80*time.Minute))
	if p, _ := src.RecentlyPlugged(ctx, optedIn, 34.1613, -118.1676, now.Add(-2*time.Hour)); !p {
		t.Error("a charge at home an hour and a half ago did not suppress")
	}
	if p, _ := src.RecentlyPlugged(ctx, optedIn, 35.0, -118.1676, now.Add(-2*time.Hour)); p {
		t.Error("a charge far from home suppressed")
	}
}
