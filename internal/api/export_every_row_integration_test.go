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
		($1,$2,'d-sub-a',$3::timestamptz,$3::timestamptz + interval '20 seconds',0.4,50,50,'live'),
		($1,$2,'d-sub-b',$3::timestamptz + interval '1 hour',$3::timestamptz + interval '1 hour 30 seconds',0,50,50,'live'),
		($1,$2,'d-reparking',$3::timestamptz + interval '2 hours',$3::timestamptz + interval '2 hours 5 minutes',0,50,50,'live')`, src, vid, base)
	exec(`INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source) VALUES
		($1,$2,'c-sub',$3::timestamptz,$3::timestamptz + interval '15 seconds',0.2,'live')`, src, vid, base.Add(30*time.Hour))

	exec(`INSERT INTO saved_trips (user_id, name, inputs, plan) VALUES ($1,'Austin to Dallas','{"origin":"A"}','{"ok":true}'), ($1,'Weekend','{"origin":"B"}',NULL)`, src)
	exec(`INSERT INTO drive_recaps (user_id, drive_id, model, recap) VALUES ($1,'d-1','m','a recap'), ($1,'d-2','m','another')`, src)
	exec(`INSERT INTO drive_efficiency (user_id, drive_id, model, analysis_text, result_json) VALUES ($1,'d-1','m','text','{"a":1}')`, src)
	exec(`INSERT INTO milestones (user_id, vehicle_id, key, status, detail, dismissed_at) VALUES ($1,$2,'miles:1000','achieved','{"x":1}',now()), ($1,$2,'miles:5000','backfilled','{}',NULL)`, src, vid)

	exec(`INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider, temp_c) VALUES ($1,'d-1',30.3,-97.7,$2::timestamptz,'open-meteo',5), ($1,'d-2',30.3,-97.7,$2::timestamptz,'open-meteo',6)`, src, base)
	exec(`INSERT INTO drive_weather_series (user_id, drive_id, sampled_at, cadence_minutes, coarse_lat, coarse_lon, provider) VALUES ($1,'d-1',$2::timestamptz,60,30.3,-97.7,'open-meteo'), ($1,'d-1',$2::timestamptz + interval '1 hour',60,30.3,-97.7,'open-meteo'), ($1,'d-2',$2::timestamptz,60,30.3,-97.7,'open-meteo')`, src, base)

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
	extraTables := map[string]string{"saved_trips": "saved_trips", "drive_recaps": "drive_recaps", "drive_analyses": "drive_efficiency", "milestones": "milestones"}
	wantExtra := map[string]int{}
	for key, table := range extraTables {
		wantExtra[key] = count(src, table)
	}
	if wantExtra["saved_trips"] != 2 || wantExtra["drive_recaps"] != 2 || wantExtra["drive_analyses"] != 1 || wantExtra["milestones"] != 2 {
		t.Fatalf("extras seed = %v", wantExtra)
	}
	wantWeather := map[string]int{"drive_weather": count(src, "drive_weather"), "drive_weather_series": count(src, "drive_weather_series")}
	if wantWeather["drive_weather"] != 2 || wantWeather["drive_weather_series"] != 3 {
		t.Fatalf("weather seed = %v", wantWeather)
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
	// Derived weather is not in the backup, and the file says so.
	var omitted []string
	if err := json.Unmarshal(raw["derived_omitted"], &omitted); err != nil || len(omitted) != 2 {
		t.Errorf("derived_omitted = %s (%v), want the two weather tables", raw["derived_omitted"], err)
	}
	if _, has := raw["drive_weather"]; has {
		t.Error("the backup carries drive_weather, which it says it omits")
	}
	for key, n := range wantExtra {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw[key], &arr); err != nil || len(arr) != n {
			t.Errorf("backup %s = %d rows (%v), the table holds %d", key, len(arr), err, n)
		}
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
	var ex struct {
		Drives, Charges []json.RawMessage
		SavedTrips      []json.RawMessage `json:"saved_trips"`
		DriveRecaps     []json.RawMessage `json:"drive_recaps"`
		DriveAnalyses   []json.RawMessage `json:"drive_analyses"`
		Milestones      []json.RawMessage `json:"milestones"`
		Weather         []json.RawMessage `json:"drive_weather"`
		WeatherSeries   []json.RawMessage `json:"drive_weather_series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Weather) != wantWeather["drive_weather"] || len(ex.WeatherSeries) != wantWeather["drive_weather_series"] {
		t.Errorf("export weather = %d drive rows, %d series rows; the tables hold %v", len(ex.Weather), len(ex.WeatherSeries), wantWeather)
	}
	if len(ex.SavedTrips) != wantExtra["saved_trips"] || len(ex.DriveRecaps) != wantExtra["drive_recaps"] ||
		len(ex.DriveAnalyses) != wantExtra["drive_analyses"] || len(ex.Milestones) != wantExtra["milestones"] {
		t.Errorf("export extras = %d trips, %d recaps, %d analyses, %d milestones; the tables hold %v",
			len(ex.SavedTrips), len(ex.DriveRecaps), len(ex.DriveAnalyses), len(ex.Milestones), wantExtra)
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
		for key, table := range extraTables {
			if got := count(dst, table); got != wantExtra[key] {
				t.Errorf("after restore %d: %s has %d rows, want %d", i, table, got, wantExtra[key])
			}
		}
	}
}
