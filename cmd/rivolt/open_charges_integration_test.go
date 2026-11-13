//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/testpg"
)

// The grant path on a real database: the role exists before 0124 runs, sees the five columns of every user's charges, nothing else, and
// the snapshot/verify subcommand works through it.
func TestOpenChargesThroughVerifyRole(t *testing.T) {
	ctx := context.Background()
	dsn := testpg.DSN(ctx, t)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rivolt_verify') THEN CREATE ROLE rivolt_verify LOGIN PASSWORD 'v' CONNECTION LIMIT 2; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	mig, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "migrations", "0124_verify_role_grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, string(mig)); err != nil {
		t.Fatal(err)
	}

	var ids [2]string
	for i := range ids {
		k := strconv.Itoa(i)
		var uid, vid string
		if err := admin.QueryRowContext(ctx, `INSERT INTO users (id, username) VALUES (gen_random_uuid(), 'u'||$1::text) RETURNING id::text`, k).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1::uuid, 'v'||$2::text) RETURNING id::text`, uid, k).Scan(&vid); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRowContext(ctx, `INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, final_state, source, notes)
			VALUES ($1::uuid, $2::uuid, 'e'||$3::text, now() - interval '1 hour', now(), 'charging_active', 'test', 'private') RETURNING id::text`, uid, vid, k).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}

	vdsn := strings.Replace(dsn, "rivolt:rivolt@", "rivolt_verify:v@", 1)
	v, err := sql.Open("pgx", vdsn)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var n int
	if err := v.QueryRowContext(ctx, `SELECT count(*) FROM charges`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows visible to the verify role across users: %d, %v (want 2)", n, err)
	}
	if err := v.QueryRowContext(ctx, `SELECT id, vehicle_id, started_at, ended_at, final_state FROM charges LIMIT 1`).Scan(new(string), new(string), new(any), new(any), new(string)); err != nil {
		t.Errorf("granted columns: %v", err)
	}
	for _, col := range []string{"notes", "user_id", "lat", "metadata"} {
		if _, err := v.ExecContext(ctx, `SELECT `+col+` FROM charges`); err == nil {
			t.Errorf("column %s is readable by the verify role", col)
		}
	}
	if _, err := v.ExecContext(ctx, `SELECT 1 FROM users`); err == nil {
		t.Error("users is readable by the verify role")
	}

	t.Setenv("RIVOLT_VERIFY_DATABASE_URL", vdsn)
	snap := filepath.Join(t.TempDir(), "s.json")
	var out, errb bytes.Buffer
	if code := runOpenCharges([]string{"snapshot", "--out", snap}, &out, &errb); code != 0 {
		t.Fatalf("snapshot exit %d: %s", code, errb.String())
	}
	out.Reset()
	if code := runOpenCharges([]string{"verify", "--snapshot", snap}, &out, &errb); code != 0 || strings.Count(out.String(), "one row") != 2 {
		t.Fatalf("verify exit %d: %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := runOpenCharges([]string{"verify", "--snapshot", snap, "--roll-hash", "h2"}, &out, &errb); code != 1 {
		t.Errorf("a snapshot without this roll's hash: exit %d, want 1", code)
	}
	if _, err := admin.ExecContext(ctx, `DELETE FROM charges WHERE id = $1::uuid`, ids[0]); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runOpenCharges([]string{"verify", "--snapshot", snap}, &out, &errb); code != 1 || !strings.Contains(out.String(), "gone") {
		t.Errorf("a vanished charge: exit %d: %s", code, out.String())
	}
}
