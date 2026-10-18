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

// A 250k-sample backup restores with flat memory, gzip or plain, and every
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
	const n = 250_000
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
	if rec.Code != 200 {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body.String())
	}
	var got restoreResult
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Samples != n || got.Drives != 20 || got.Charges != 10 {
		t.Fatalf("result = %+v", got)
	}
	for table, want := range map[string]int{"vehicle_state": n, "drives": 20, "charges": 10} {
		var c int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, dst).Scan(&c); err != nil || c != want {
			t.Errorf("%s rows = %d (%v), want %d", table, c, err, want)
		}
	}
	delta := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("restored %d samples from %d MB on the wire, peak heap delta %d MB", n, wire.Load()>>20, delta>>20)
	if delta > 50<<20 {
		t.Errorf("heap grew by %d MB during the restore; want under 50", delta>>20)
	}

	// Run again right away: refused with Retry-After (the user gap).
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/api/data/restore", strings.NewReader(`{}`)).WithContext(auth.WithUser(ctx, dst)))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("repeat = %d Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
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
	restore := func(uid uuid.UUID, body io.Reader) (int, restoreResult) {
		rec := httptest.NewRecorder()
		handleDataRestore(deps)(rec, httptest.NewRequest("POST", "/api/data/restore", body).WithContext(auth.WithUser(ctx, uid)))
		var r restoreResult
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return rec.Code, r
	}

	// Plain JSON (what older backups are).
	var plain bytes.Buffer
	if err := genBackup(&plain, "rv-gen", 3000, false); err != nil {
		t.Fatal(err)
	}
	if code, r := restore(mkDst("rs-plain"), bytes.NewReader(plain.Bytes())); code != 200 || r.Samples != 3000 || r.Drives != 20 || r.Charges != 10 {
		t.Fatalf("plain restore = %d %+v", code, r)
	}
	// Key order and null arrays (an older, alphabetical backup with no drives).
	old := `{"charges":null,"created_at":"x","drives":null,"samples":[` + sampleJSON(t, "rv-gen", 5) + `],"user_settings":{"a":"b"},"version":"v0","vehicle_profiles":[]}`
	if code, r := restore(mkDst("rs-old"), strings.NewReader(old)); code != 200 || r.Samples != 5 {
		t.Fatalf("old-format restore = %d %+v", code, r)
	}
	// Not a backup, and truncated JSON: 400, not a panic and not a success.
	for name, bad := range map[string]string{"array": `[1,2]`, "truncated": `{"samples":[{"VehicleID":"rv-gen"`, "garbage": `hello`} {
		if code, _ := restore(mkDst("rs-bad-"+name), strings.NewReader(bad)); code != http.StatusBadRequest {
			t.Errorf("%s restore = %d, want 400", name, code)
		}
	}
	// Too big once decompressed: 413.
	restoreMaxPlain = 64 << 10
	defer func() { restoreMaxPlain = 2 << 30 }()
	if code, _ := restore(mkDst("rs-big"), bytes.NewReader(plain.Bytes())); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized restore = %d, want 413", code)
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
