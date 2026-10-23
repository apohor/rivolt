//go:build integration

package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// Migration 0082 removes the stored release-notes links (one-hour signed URLs with temporary credentials) and leaves
// has_notes where a link existed; the rest of the OTA record is untouched.
func TestMigration0082RemovesTheStoredNotesLinks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "ota-migration")
	if err != nil {
		t.Fatal(err)
	}
	const link = "https://docs.s3.amazonaws.com/x.pdf?X-Amz-Security-Token=SECRETTOKEN"
	seed := func(rid, ota string) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model, metadata) VALUES ($1,$2,'R1T',$3::jsonb)`, uid, rid, `{"keep":1,"ota":`+ota+`}`); err != nil {
			t.Fatal(err)
		}
	}
	seed("ota-both", `{"current":{"version":"1","release_notes_url":"`+link+`","locale":"en-US"},"available":{"version":"2","release_notes_url":"`+link+`"},"fetched_at":"2026-10-17T10:00:00Z"}`)
	seed("ota-current", `{"current":{"version":"1","release_notes_url":"`+link+`"},"available":{"version":"2"}}`)
	seed("ota-none", `{"current":{"version":"1"},"fetched_at":"2026-10-17T10:00:00Z"}`)
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'ota-nometa','R1T')`, uid); err != nil {
		t.Fatal(err)
	}

	sqlText, err := os.ReadFile("../db/migrations/0082_ota_notes_not_stored.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, string(sqlText)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var leftovers int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE metadata::text LIKE '%release_notes_url%' OR metadata::text LIKE '%SECRETTOKEN%'`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Errorf("%d rows still hold a stored link", leftovers)
	}
	check := func(rid, query string, want any) {
		t.Helper()
		var got any
		if err := pool.QueryRowContext(ctx, query, uid, rid).Scan(&got); err != nil {
			t.Fatalf("%s: %v", rid, err)
		}
		if gs, ws := toString(got), toString(want); gs != ws {
			t.Errorf("%s: %s = %v, want %v", rid, query, gs, ws)
		}
	}
	for _, c := range []struct {
		rid, q string
		want   any
	}{
		{"ota-both", `SELECT metadata#>>'{ota,current,has_notes}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "true"},
		{"ota-both", `SELECT metadata#>>'{ota,available,has_notes}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "true"},
		{"ota-both", `SELECT metadata#>>'{ota,current,locale}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "en-US"},
		{"ota-both", `SELECT metadata#>>'{ota,fetched_at}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "2026-10-17T10:00:00Z"},
		{"ota-both", `SELECT metadata->>'keep' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "1"},
		{"ota-current", `SELECT metadata#>>'{ota,current,has_notes}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "true"},
		{"ota-current", `SELECT metadata#>>'{ota,available,has_notes}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, nil},
		{"ota-current", `SELECT metadata#>>'{ota,available,version}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "2"},
		{"ota-none", `SELECT metadata#>>'{ota,current,has_notes}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, nil},
		{"ota-none", `SELECT metadata#>>'{ota,current,version}' FROM vehicles WHERE user_id=$1 AND rivian_vehicle_id=$2`, "1"},
	} {
		check(c.rid, c.q, c.want)
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case []byte:
		return string(x)
	case string:
		return x
	}
	return "?"
}
