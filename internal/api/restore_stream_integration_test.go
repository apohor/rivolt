//go:build integration

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// genBackup writes a backup document of n samples (plus a few drives and
// charges) into w as it goes, optionally gzipped, without holding it in memory.
func genBackup(w io.Writer, vehicle string, n int, gzipped bool) error {
	var out io.Writer = w
	var zw *gzip.Writer
	if gzipped {
		zw, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
		out = zw
	}
	enc := json.NewEncoder(out)
	put := func(s string) { _, _ = io.WriteString(out, s) }
	put(`{"version":"gen","created_at":"2026-10-17T00:00:00Z","user_settings":{},"vehicle_profiles":[],"drives":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			put(",")
		}
		start := time.Date(2026, 3, 1, i, 0, 0, 0, time.UTC)
		_ = enc.Encode(map[string]any{"ID": "gd-" + string(rune('a'+i)), "VehicleID": vehicle, "StartedAt": start, "EndedAt": start.Add(30 * time.Minute), "DistanceMi": 12.5, "Source": "live"})
	}
	put(`],"charges":[`)
	for i := 0; i < 10; i++ {
		if i > 0 {
			put(",")
		}
		start := time.Date(2026, 3, 2, i, 0, 0, 0, time.UTC)
		_ = enc.Encode(map[string]any{"ID": "gc-" + string(rune('a'+i)), "VehicleID": vehicle, "StartedAt": start, "EndedAt": start.Add(time.Hour), "EnergyAddedKWh": 30, "Source": "live"})
	}
	put(`],"samples":[`)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		if i > 0 {
			put(",")
		}
		if err := enc.Encode(samples.Sample{VehicleID: vehicle, At: base.Add(time.Duration(i) * time.Minute), BatteryLevelPct: 20 + float64(i%70),
			RangeMi: 200, OdometerMi: 1000 + float64(i)/10, Lat: 39.5, Lon: -119.8, SpeedMph: float64(i % 80), ShiftState: "D", ChargingState: "not_charging", Source: "live"}); err != nil {
			return err
		}
	}
	put(`]}`)
	if zw != nil {
		return zw.Close()
	}
	return nil
}

// A 100k-sample backup restores with flat memory, gzip or plain, and every
// row lands.
func TestRestoreStreamsWithBoundedMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	dst, err := db.EnsureUser(ctx, pool, "rs-dst")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-gen','R1S')`, dst); err != nil {
		t.Fatal(err)
	}
	const n = 100_000
	restoreHeartbeat = 100 * time.Millisecond
	defer func() { restoreHeartbeat = 3 * time.Second }()
	h := handleDataRestore(backupTestDeps(pool))

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()

	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(genBackup(pw, "rv-gen", n, true)) }()
	var wire atomic.Int64
	body := io.TeeReader(pr, countWriter{&wire})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/api/data/restore", body).WithContext(auth.WithUser(ctx, dst)))
	close(stop)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("restore = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	ev, _, got := final(t, rec)
	if ev != "done" || got.Samples != n || got.Drives != 20 || got.Charges != 10 {
		t.Fatalf("final event %q result = %+v", ev, got)
	}
	// The stream starts with "start", reports progress while it works (counts
	// never go backwards) and ends with "done".
	all := events(t, rec.Body.Bytes())
	if all[0]["event"] != "start" {
		t.Errorf("first event = %v", all[0]["event"])
	}
	progress, last := 0, -1.0
	for _, e := range all {
		if e["event"] == "progress" {
			progress++
			if s, _ := e["samples"].(float64); s < last {
				t.Errorf("progress went backwards: %v after %v", s, last)
			} else {
				last = s
			}
		}
	}
	if progress < 2 {
		t.Errorf("only %d progress events for a long restore", progress)
	}
	for table, want := range map[string]int{"vehicle_state": n, "drives": 20, "charges": 10} {
		var c int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, dst).Scan(&c); err != nil || c != want {
			t.Errorf("%s rows = %d (%v), want %d", table, c, err, want)
		}
	}
	delta := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("restored %d samples from %d MB on the wire, peak heap delta %d MB", n, wire.Load()>>20, delta>>20)
	// Streaming keeps the heap flat; decoding the whole backup into slices, as the
	// old handler did, costs 30 MB or more for 100k samples.
	if delta > 15<<20 {
		t.Errorf("heap grew by %d MB during the restore; want under 15", delta>>20)
	}

	// Run again right away: refused with Retry-After (the user gap).
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/api/data/restore", strings.NewReader(`{}`)).WithContext(auth.WithUser(ctx, dst)))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("repeat = %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// events parses an NDJSON restore response.
func events(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// final is the last event of a restore: its name, the HTTP-style status of an
// error, and the counts.
func final(t *testing.T, rec *httptest.ResponseRecorder) (event string, status int, r restoreResult) {
	t.Helper()
	ev := events(t, rec.Body.Bytes())
	if len(ev) == 0 {
		t.Fatalf("no events in %q (HTTP %d)", rec.Body.String(), rec.Code)
	}
	last := ev[len(ev)-1]
	event, _ = last["event"].(string)
	if st, ok := last["status"].(float64); ok {
		status = int(st)
	}
	num := func(k string) int { f, _ := last[k].(float64); return int(f) }
	return event, status, restoreResult{Drives: num("drives"), Charges: num("charges"), Samples: num("samples"), Skipped: num("skipped")}
}

type countWriter struct{ n *atomic.Int64 }

func (c countWriter) Write(p []byte) (int, error) { c.n.Add(int64(len(p))); return len(p), nil }

// backup (gzip by default) -> restore into another account, plain JSON still
// restores, key order does not matter, and an oversized backup is refused.
func TestRestoreFormatsAndLimits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	deps := backupTestDeps(pool)
	mkDst := func(name string) uuid.UUID {
		u, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-gen','R1S')`, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	// restore returns the final event ("done" or "error"), the status carried
	// by an error event, and the counts.
	restore := func(uid uuid.UUID, body io.Reader) (string, int, restoreResult) {
		rec := httptest.NewRecorder()
		handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", body).WithContext(auth.WithUser(ctx, uid)))
		ev, st, r := final(t, rec)
		return ev, st, r
	}

	// Plain JSON (what older backups are).
	var plain bytes.Buffer
	if err := genBackup(&plain, "rv-gen", 3000, false); err != nil {
		t.Fatal(err)
	}
	if ev, _, r := restore(mkDst("rs-plain"), bytes.NewReader(plain.Bytes())); ev != "done" || r.Samples != 3000 || r.Drives != 20 || r.Charges != 10 {
		t.Fatalf("plain restore = %s %+v", ev, r)
	}
	// Key order and null arrays (an older, alphabetical backup with no drives).
	old := `{"charges":null,"created_at":"x","drives":null,"samples":[` + sampleJSON(t, "rv-gen", 5) + `],"user_settings":{"a":"b"},"version":"v0","vehicle_profiles":[]}`
	if ev, _, r := restore(mkDst("rs-old"), strings.NewReader(old)); ev != "done" || r.Samples != 5 {
		t.Fatalf("old-format restore = %s %+v", ev, r)
	}
	// Not a backup, and truncated JSON: 400, not a panic and not a success.
	for name, bad := range map[string]string{"array": `[1,2]`, "truncated": `{"samples":[{"VehicleID":"rv-gen"`, "garbage": `hello`} {
		if ev, st, _ := restore(mkDst("rs-bad-"+name), strings.NewReader(bad)); ev != "error" || st != http.StatusBadRequest {
			t.Errorf("%s restore = %s %d, want an error event with status 400", name, ev, st)
		}
	}
	// Too big once decompressed: 413.
	restoreMaxPlain = 64 << 10
	defer func() { restoreMaxPlain = 2 << 30 }()
	if ev, st, _ := restore(mkDst("rs-big"), bytes.NewReader(plain.Bytes())); ev != "error" || st != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized restore = %s %d, want an error event with status 413", ev, st)
	}
}

func sampleJSON(t *testing.T, vehicle string, n int) string {
	t.Helper()
	var parts []string
	for i := 0; i < n; i++ {
		b, err := json.Marshal(samples.Sample{VehicleID: vehicle, At: time.Date(2026, 3, 1, 0, i, 0, 0, time.UTC), Source: "live"})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(b))
	}
	return strings.Join(parts, ",")
}

// A crafted file cannot create partitions or vehicles: rows outside
// 2021-01-01..now+24h are skipped before any partition is made, and a
// restore may add at most five vehicles.
func TestRestoreRefusesPartitionAndVehicleFlood(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	deps := backupTestDeps(pool)
	partitions := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent WHERE p.relname = 'vehicle_state'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	user := func(name string) uuid.UUID {
		u, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	restore := func(uid uuid.UUID, body string) (string, int, restoreResult) {
		rec := httptest.NewRecorder()
		handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", strings.NewReader(body)).WithContext(auth.WithUser(ctx, uid)))
		return final(t, rec)
	}
	row := func(vehicle string, at time.Time) string {
		b, _ := json.Marshal(samples.Sample{VehicleID: vehicle, At: at, Source: "live"})
		return string(b)
	}

	// Samples spread over 600 months (back to 1976 and out to 2076), plus one good row.
	var rows []string
	for m := -600; m < 600; m += 12 {
		rows = append(rows, row("rv-flood", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, m, 0)))
	}
	rows = append(rows, row("rv-flood", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)), row("rv-flood", time.Time{}))
	before := partitions()
	ev, _, r := restore(user("rf-1"), `{"samples":[`+strings.Join(rows, ",")+`]}`)
	if ev != "done" || r.Samples == 0 || r.Skipped == 0 {
		t.Fatalf("restore = %s %+v", ev, r)
	}
	// Only in-range months can have been created; none for 1976..2020 or 2027+.
	var oldest, newest time.Time
	if err := pool.QueryRowContext(ctx, `SELECT min(at), max(at) FROM vehicle_state`).Scan(&oldest, &newest); err != nil {
		t.Fatal(err)
	}
	if oldest.Before(restoreMinTime) || newest.After(time.Now().Add(48*time.Hour)) {
		t.Errorf("out-of-range rows were stored: %v .. %v", oldest, newest)
	}
	if grew := partitions() - before; grew > 8 {
		t.Errorf("restore created %d partitions", grew)
	}

	// Vehicles: five new ones are fine, a sixth is refused.
	five := []string{row("rv-n1", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)), row("rv-n2", time.Date(2026, 3, 1, 0, 1, 0, 0, time.UTC)),
		row("rv-n3", time.Date(2026, 3, 1, 0, 2, 0, 0, time.UTC)), row("rv-n4", time.Date(2026, 3, 1, 0, 3, 0, 0, time.UTC)), row("rv-n5", time.Date(2026, 3, 1, 0, 4, 0, 0, time.UTC))}
	if ev, _, r := restore(user("rf-5"), `{"samples":[`+strings.Join(five, ",")+`]}`); ev != "done" || r.Samples != 5 {
		t.Fatalf("five vehicles = %s %+v", ev, r)
	}
	six := append(append([]string{}, five...), row("rv-n6", time.Date(2026, 3, 1, 0, 5, 0, 0, time.UTC)))
	u6 := user("rf-6")
	if ev, st, _ := restore(u6, `{"samples":[`+strings.Join(six, ",")+`]}`); ev != "error" || st != http.StatusBadRequest {
		t.Fatalf("six vehicles = %s %d, want an error event with status 400", ev, st)
	}
	var nv int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM vehicles WHERE user_id = $1`, u6).Scan(&nv); err != nil || nv > 5 {
		t.Errorf("vehicles created for the refused restore = %d (%v), want at most 5", nv, err)
	}
}

// A file that is not a backup, or is damaged, is refused with a fixed sentence
// (never the decoder's text) and does not use up the user's slot; neither does
// a valid backup that wrote nothing.
func TestRestoreRefusalsDoNotConsumeTheSlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	deps := backupTestDeps(pool)
	uid, err := db.EnsureUser(ctx, pool, "slot-user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-gen','R1S')`, uid); err != nil {
		t.Fatal(err)
	}
	restore := func(body io.Reader) (*httptest.ResponseRecorder, map[string]any) {
		rec := httptest.NewRecorder()
		handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", body).WithContext(auth.WithUser(ctx, uid)))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("the slot was already used: %s", rec.Body.String())
		}
		ev := events(t, rec.Body.Bytes())
		return rec, ev[len(ev)-1]
	}

	for name, body := range map[string]string{
		"valid json, not a backup": `{"hello":"world"}`,
		"junk":                     `hello world`,
		"truncated":                `{"samples":[{"VehicleID":"rv-gen"`,
		"array":                    `[1,2,3]`,
	} {
		_, last := restore(strings.NewReader(body))
		want := msgDamaged
		if name == "valid json, not a backup" {
			want = msgNotBackup
		}
		if last["event"] != "error" || last["status"] != float64(400) || last["error"] != want {
			t.Errorf("%s: %v, want a 400 error event saying %q", name, last, want)
		}
	}
	// Corrupt gzip (a good header, a damaged body): the same sentence, either as
	// a plain 400 up front or as an error event once the stream has started,
	// and never the decompressor's text.
	rec := httptest.NewRecorder()
	handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", bytes.NewReader([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff, 1, 2, 3})).WithContext(auth.WithUser(ctx, uid)))
	if !strings.Contains(rec.Body.String(), msgDamaged) || strings.Contains(rec.Body.String(), "flate") || strings.Contains(rec.Body.String(), "gzip:") || strings.Contains(rec.Body.String(), "EOF") {
		t.Errorf("corrupt gzip = %d %q", rec.Code, rec.Body.String())
	}

	// A valid backup with no rows is a success that writes nothing: no wait.
	if _, last := restore(strings.NewReader(`{"version":"x","drives":[],"charges":[],"samples":[]}`)); last["event"] != "done" {
		t.Errorf("empty backup: %v", last)
	}
	// Still free: a real one goes through, and now it does use the slot.
	var real bytes.Buffer
	if err := genBackup(&real, "rv-gen", 100, false); err != nil {
		t.Fatal(err)
	}
	if _, last := restore(bytes.NewReader(real.Bytes())); last["event"] != "done" || last["samples"] != float64(100) {
		t.Fatalf("real backup: %v", last)
	}
	rec = httptest.NewRecorder()
	handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", strings.NewReader(`{}`)).WithContext(auth.WithUser(ctx, uid)))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("after a restore that wrote rows, the next = %d, want 429", rec.Code)
	}
}

// Through the handler: a refused file frees the slot but a second upload right
// away waits the cooldown (429), and after it the user may try again.
func TestRestoreRefusalCooldownThroughTheHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	old := restoreRefusalCooldown
	restoreRefusalCooldown = 300 * time.Millisecond
	defer func() { restoreRefusalCooldown = old }()
	uid, err := db.EnsureUser(ctx, pool, "cool-user")
	if err != nil {
		t.Fatal(err)
	}
	h := handleDataRestore(backupTestDeps(pool)) // one handler: one guard
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/api/data/restore", strings.NewReader(body)).WithContext(auth.WithUser(ctx, uid)))
		return rec
	}
	if rec := post(`{"hello":"world"}`); !strings.Contains(rec.Body.String(), msgNotBackup) {
		t.Fatalf("first upload: %q", rec.Body.String())
	}
	rec := post(`{"hello":"again"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("immediate second upload = %d, want 429 with Retry-After", rec.Code)
	}
	time.Sleep(400 * time.Millisecond)
	if rec := post(`{"version":"x","drives":[],"charges":[],"samples":[]}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"event":"done"`) {
		t.Fatalf("after the cooldown: %d %q", rec.Code, rec.Body.String())
	}
	// An empty restore also cools down.
	if rec := post(`{"samples":[]}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("right after an empty restore = %d, want 429", rec.Code)
	}
}
