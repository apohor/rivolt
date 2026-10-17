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
// property, nothing for the demo user, nothing with events off, and nothing
// for a request that sent Do Not Track or Global Privacy Control. Rows carry anon_id 'server' and the user id.
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

	// Do Not Track and Global Privacy Control: a request carrying either signal
	// records no row; a normal request records one. (The api middleware sets
	// the same context flag from the DNT and Sec-GPC headers.)
	for name, c := range map[string]context.Context{"dnt/gpc": db.WithDoNotTrack(ctx), "normal": ctx} {
		u, err := db.EnsureUserVerified(c, pool, "funnel-"+name, name+"@x.test", "X", false)
		if err != nil {
			t.Fatal(err)
		}
		db.RecordServerEvent(c, pool, u, db.EventRivianConnectOK, map[string]any{"vehicle_count": 1})
		want := 0
		if name == "normal" {
			want = 1
		}
		if got := count(u, db.EventSignupComplete); got != want {
			t.Errorf("%s: signup_complete = %d, want %d", name, got, want)
		}
		if got := count(u, db.EventRivianConnectOK); got != want {
			t.Errorf("%s: rivian_connect_ok = %d, want %d", name, got, want)
		}
	}
}
