//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// TestGrantReadonly pins the reporting role's allowlist against a real
// schema: every listed table is readable, the secret-bearing ones and
// signup_requests.signup_token are not, and a missing role is a no-op
// rather than an error (CNPG creates the role on its own schedule).
func TestGrantReadonly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	if ok, err := db.GrantReadonly(ctx, pool, "grafana_ro"); err != nil || ok {
		t.Fatalf("missing role: GrantReadonly = %v, %v; want false, nil", ok, err)
	}
	if _, err := db.GrantReadonly(ctx, pool, "bad;name"); err == nil {
		t.Fatal("invalid role name accepted")
	}

	if _, err := pool.ExecContext(ctx, `CREATE ROLE grafana_ro NOLOGIN`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent: the boot loop re-runs it
		if ok, err := db.GrantReadonly(ctx, pool, "grafana_ro"); err != nil || !ok {
			t.Fatalf("GrantReadonly run %d = %v, %v; want true, nil", i+1, ok, err)
		}
	}

	tablePriv := func(table string) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRowContext(ctx,
			`SELECT has_table_privilege('grafana_ro', $1, 'SELECT')`, table).Scan(&ok); err != nil {
			t.Fatalf("has_table_privilege(%s): %v", table, err)
		}
		return ok
	}
	for _, tbl := range []string{"users", "vehicles", "drives", "charges", "vehicle_state", "flags"} {
		if !tablePriv(tbl) {
			t.Errorf("%s: want readable", tbl)
		}
	}
	for _, tbl := range []string{"user_secrets", "sessions", "push_subscriptions", "push_vapid", "app_settings", "user_settings", "signup_requests"} {
		if tablePriv(tbl) {
			t.Errorf("%s: must not be readable as a whole table", tbl)
		}
	}

	colPriv := func(col string) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRowContext(ctx,
			`SELECT has_column_privilege('grafana_ro', 'signup_requests', $1, 'SELECT')`, col).Scan(&ok); err != nil {
			t.Fatalf("has_column_privilege(%s): %v", col, err)
		}
		return ok
	}
	if !colPriv("email") || !colPriv("status") {
		t.Error("signup_requests email/status: want readable")
	}
	if colPriv("signup_token") {
		t.Error("signup_requests.signup_token must not be readable")
	}

	// The connected-users view: readable as the reporting role, the
	// boolean is right, and user_secrets itself stays closed.
	connected, err := db.EnsureUser(ctx, pool, "ro-connected")
	if err != nil {
		t.Fatal(err)
	}
	notConnected, err := db.EnsureUser(ctx, pool, "ro-not-connected")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext) VALUES ($1, 'rivian.session', '\x00')`, connected); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET ROLE grafana_ro`); err != nil {
		t.Fatalf("set role: %v", err)
	}
	has := func(uid any) bool {
		t.Helper()
		var b bool
		if err := conn.QueryRowContext(ctx, `SELECT has_rivian_session FROM user_rivian_connected WHERE user_id = $1`, uid).Scan(&b); err != nil {
			t.Fatalf("view as grafana_ro: %v", err)
		}
		return b
	}
	if !has(connected) || has(notConnected) {
		t.Error("user_rivian_connected: wrong booleans")
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM user_secrets`).Scan(&n); err == nil {
		t.Error("grafana_ro read user_secrets directly")
	}
	_, _ = conn.ExecContext(ctx, `RESET ROLE`)
}
