//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// Server-side funnel events in Postgres: signup_complete once per new
// account (not on a repeat sign-in), rivian_connect_ok with only the allowed
// property, first_drive_recorded once per user, nothing for the demo user, and
// nothing at all with events off. Rows carry anon_id 'server' and the user id.
func TestServerFunnelEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	count := func(uid uuid.UUID, name string) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE user_id = $1 AND name = $2 AND anon_id = 'server'`, uid, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Events off: a new account records nothing.
	t.Setenv("RIVOLT_EVENTS", "")
	off, err := db.EnsureUserVerified(db.WithSignupMethod(ctx, "oidc"), pool, "funnel-off", "off@x.test", "Off", false)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(off, db.EventSignupComplete); n != 0 {
		t.Fatalf("recorded with events off: %d", n)
	}

	t.Setenv("RIVOLT_EVENTS", "true")
	uid, err := db.EnsureUserVerified(db.WithSignupMethod(ctx, "oidc"), pool, "funnel-user", "f@x.test", "F", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureUserVerified(ctx, pool, "funnel-user", "f@x.test", "F", true); err != nil { // repeat sign-in
		t.Fatal(err)
	}
	if n := count(uid, db.EventSignupComplete); n != 1 {
		t.Fatalf("signup_complete = %d, want 1", n)
	}
	var method string
	_ = pool.QueryRowContext(ctx, `SELECT props->>'method' FROM events WHERE user_id = $1 AND name = $2`, uid, db.EventSignupComplete).Scan(&method)
	if method != "oidc" {
		t.Errorf("method = %q", method)
	}

	// The demo account never records.
	demoID, err := db.EnsureUserFull(ctx, pool, db.DemoUsername, "", "Demo")
	if err != nil {
		t.Fatal(err)
	}
	db.RecordServerEvent(ctx, pool, demoID, db.EventRivianConnectOK, nil)
	if n := count(demoID, db.EventSignupComplete) + count(demoID, db.EventRivianConnectOK); n != 0 {
		t.Errorf("demo user recorded %d events", n)
	}

	// rivian_connect_ok keeps only vehicle_count.
	db.RecordServerEvent(ctx, pool, uid, db.EventRivianConnectOK, map[string]any{"vehicle_count": 2, "vin": "SHOULDNOTSTORE"})
	var props string
	_ = pool.QueryRowContext(ctx, `SELECT props::text FROM events WHERE user_id = $1 AND name = $2`, uid, db.EventRivianConnectOK).Scan(&props)
	if props != `{"vehicle_count": 2}` {
		t.Errorf("props = %s", props)
	}

	// first_drive_recorded: only for the first drive, only once.
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-f') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	drive := func(ext string, ago time.Duration) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4,$5,5,'live')`,
			uid, vid, ext, time.Now().Add(-ago), time.Now().Add(-ago+10*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	drive("d1", 3*time.Hour)
	db.RecordFirstDrive(ctx, pool, uid)
	db.RecordFirstDrive(ctx, pool, uid)
	drive("d2", time.Hour)
	db.RecordFirstDrive(ctx, pool, uid)
	if n := count(uid, db.EventFirstDriveRecorded); n != 1 {
		t.Errorf("first_drive_recorded = %d, want 1", n)
	}
	// After the 90-day purge removes the event, a later drive does not re-fire it.
	if _, err := pool.ExecContext(ctx, `DELETE FROM events WHERE user_id = $1 AND name = $2`, uid, db.EventFirstDriveRecorded); err != nil {
		t.Fatal(err)
	}
	drive("d3", 30*time.Minute)
	db.RecordFirstDrive(ctx, pool, uid)
	if n := count(uid, db.EventFirstDriveRecorded); n != 0 {
		t.Errorf("fired again for a user with several drives: %d", n)
	}
}
