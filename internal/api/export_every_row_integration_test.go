//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// The backup and the data export hold every stored row, not the rows the lists
// show: sub-minute drives and charges and re-parking rows are in them. For a
// seeded user the row count of each array equals SELECT count(*) of its table,
// and restoring the backup (twice) puts every row back exactly once.
func TestBackupAndExportHoldEveryStoredRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
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
	src := seedBackupUser(t, ctx, pool, "every-row", 40, 3, 2)
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `SELECT id FROM vehicles WHERE user_id = $1`, src).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	// What the lists hide: a 20 s drive with real distance, a 30 s one, a re-parking
	// row (no distance, same SoC, over a minute), and a 15 s charge.
	exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, start_soc_pct, end_soc_pct, source) VALUES
		($1,$2,'d-sub-a',$3,$3 + interval '20 seconds',0.4,50,50,'live'),
		($1,$2,'d-sub-b',$3 + interval '1 hour',$3 + interval '1 hour 30 seconds',0,50,50,'live'),
		($1,$2,'d-reparking',$3 + interval '2 hours',$3 + interval '2 hours 5 minutes',0,50,50,'live')`, src, vid, base)
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source) VALUES
		($1,$2,'c-sub',$3,$3 + interval '15 seconds',0.2,'live')`, src, vid, base.Add(30*time.Hour))

	count := func(uid uuid.UUID, table string) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	want := map[string]int{"drives": count(src, "drives"), "charges": count(src, "charges"), "samples": count(src, "vehicle_state")}
	if want["drives"] != 6 || want["charges"] != 3 || want["samples"] != 40 {
		t.Fatalf("seed = %v", want)
	}
	deps := backupTestDeps(pool)

	// Backup.
	rec := httptest.NewRecorder()
	handleDataBackup(deps)(rec, httptest.NewRequest("GET", "/api/data/backup?gzip=0", nil).WithContext(auth.WithUser(ctx, src)))
	if rec.Code != 200 {
		t.Fatalf("backup = %d", rec.Code)
	}
	backup := append([]byte(nil), rec.Body.Bytes()...)
	var bk struct {
		Drives, Charges, Samples []json.RawMessage
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(backup, &raw); err != nil {
		t.Fatal(err)
	}
	for k, dst := range map[string]*[]json.RawMessage{"drives": &bk.Drives, "charges": &bk.Charges, "samples": &bk.Samples} {
		if err := json.Unmarshal(raw[k], dst); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if len(bk.Drives) != want["drives"] || len(bk.Charges) != want["charges"] || len(bk.Samples) != want["samples"] {
		t.Errorf("backup rows = %d drives, %d charges, %d samples; the tables hold %v", len(bk.Drives), len(bk.Charges), len(bk.Samples), want)
	}

	// Data export (JSON).
	rec = httptest.NewRecorder()
	handleExport(deps)(rec, httptest.NewRequest("GET", "/api/export?format=json", nil).WithContext(auth.WithUser(ctx, src)))
	if rec.Code != 200 {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	var ex struct{ Drives, Charges []json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Drives) != want["drives"] || len(ex.Charges) != want["charges"] {
		t.Errorf("export rows = %d drives, %d charges; the tables hold %d and %d", len(ex.Drives), len(ex.Charges), want["drives"], want["charges"])
	}

	// Restore into another account with the same truck, twice: every row lands once.
	dst, err := db.EnsureUser(ctx, pool, "every-row-dst")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-every-row','R1S')`, dst)
	restoreHeartbeat = 100 * time.Millisecond
	defer func() { restoreHeartbeat = 3 * time.Second }()
	for i := 1; i <= 2; i++ {
		exec(`UPDATE users SET last_restore_at = NULL WHERE id = $1`, dst)
		rec = httptest.NewRecorder()
		handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", bytes.NewReader(backup)).WithContext(auth.WithUser(ctx, dst)))
		if rec.Code != 200 {
			t.Fatalf("restore %d = %d: %s", i, rec.Code, rec.Body.String())
		}
		if ev, st, _ := final(t, rec); ev != "done" {
			t.Fatalf("restore %d ended %q (status %d): %s", i, ev, st, strings.TrimSpace(rec.Body.String()))
		}
		// Samples are left out of this check: a restore drops those older than the
		// install's retention, which is not what is under test.
		for table, n := range map[string]int{"drives": want["drives"], "charges": want["charges"]} {
			if got := count(dst, table); got != n {
				t.Errorf("after restore %d: %s has %d rows, want %d", i, table, got, n)
			}
		}
	}
}
