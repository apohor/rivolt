//go:build integration

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
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
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
)

// pipeResponse is a ResponseWriter whose body goes to a pipe, so a test can
// read a huge response as a stream instead of holding it.
type pipeResponse struct {
	h      http.Header
	status int
	pw     *io.PipeWriter
	n      atomic.Int64
}

func (p *pipeResponse) Header() http.Header { return p.h }
func (p *pipeResponse) WriteHeader(c int)   { p.status = c }
func (p *pipeResponse) Flush()              {}
func (p *pipeResponse) Write(b []byte) (int, error) {
	p.n.Add(int64(len(b)))
	return p.pw.Write(b)
}

func seedBackupUser(t *testing.T, ctx context.Context, pool *sql.DB, name string, nSamples, nDrives, nCharges int) uuid.UUID {
	t.Helper()
	uid, err := db.EnsureUser(ctx, pool, name)
	if err != nil {
		t.Fatal(err)
	}
	var vid uuid.UUID
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-'||$2,'R1S') RETURNING id`, uid, name).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d <= nSamples/1440+8; d += 7 { // monthly partitions cover every week
		if _, err := pool.ExecContext(ctx, `SELECT rivolt_ensure_vehicle_state_partition($1)`, base.AddDate(0, 0, d)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO vehicle_state (vehicle_id, user_id, at, source, battery_level_pct, range_mi, odometer_mi, lat, lon, speed_mph, shift_state, charging_state)
		SELECT $1, $2, $3::timestamptz + g * interval '1 minute', 'live', 20 + (g % 70), 200 + (g % 100), 1000 + g / 10.0,
		       39.5 + (g % 1000) / 10000.0, -119.8 - (g % 1000) / 10000.0, (g % 80), 'D', 'not_charging'
		  FROM generate_series(0, $4::int - 1) g`, vid, uid, base, nSamples); err != nil {
		t.Fatal(err)
	}
	poly := strings.Repeat("_p~iF~ps|U_ulLnnqC_mqNvxq`@", 20)
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source, route_polyline)
		SELECT $1, $2, 'd-'||g, $3::timestamptz + g * interval '1 hour', $3::timestamptz + g * interval '1 hour' + interval '30 minutes', 12.5, 'live', $5
		  FROM generate_series(1, $4::int) g`, uid, vid, base, nDrives, poly); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO charges (user_id, vehicle_id, external_id, started_at, ended_at, energy_added_kwh, source)
		SELECT $1, $2, 'c-'||g, $3::timestamptz + g * interval '3 hour', $3::timestamptz + g * interval '3 hour' + interval '1 hour', 30, 'live'
		  FROM generate_series(1, $4::int) g`, uid, vid, base, nCharges); err != nil {
		t.Fatal(err)
	}
	return uid
}

func backupTestDeps(pool *sql.DB) Deps {
	rf := db.NewVehicleResolverFactory(pool)
	return Deps{
		DB: pool, Version: "test",
		Drives: drives.NewFactory(pool, rf), Charges: charges.NewFactory(pool, rf),
		Samples: samples.NewFactory(pool, rf), Settings: settings.NewFactory(pool),
	}
}

// The backup is streamed: with 100k samples the heap stays flat, the output is
// one valid JSON document with every row, and a second request inside the gap
// is refused with Retry-After.
func TestBackupStreamsWithBoundedMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	const nSamples, nDrives, nCharges = 100_000, 300, 200
	uid := seedBackupUser(t, ctx, pool, "bk-big", nSamples, nDrives, nCharges)
	h := handleDataBackup(backupTestDeps(pool))

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
	resp := &pipeResponse{h: http.Header{}, pw: pw}
	req := httptest.NewRequest("GET", "/api/data/backup?gzip=0", nil).WithContext(auth.WithUser(ctx, uid))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer pw.Close()
		h(resp, req)
	}()

	// Read it as a stream: count rows of each array without keeping them.
	counts := map[string]int{}
	dec := json.NewDecoder(pr)
	expectDelim := func(want json.Delim) {
		t.Helper()
		tok, err := dec.Token()
		if err != nil || tok != want {
			t.Fatalf("token = %v, %v; want %v", tok, err, want)
		}
	}
	expectDelim('{')
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		key := keyTok.(string)
		switch key {
		case "drives", "charges", "samples":
			expectDelim('[')
			for dec.More() {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					t.Fatal(err)
				}
				counts[key]++
			}
			expectDelim(']')
		default:
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			counts[key] = len(raw)
		}
	}
	expectDelim('}')
	<-done
	close(stop)

	if resp.status != 200 || counts["samples"] != nSamples || counts["drives"] != nDrives || counts["charges"] != nCharges {
		t.Fatalf("status %d counts %v; want %d samples, %d drives, %d charges", resp.status, counts, nSamples, nDrives, nCharges)
	}
	if counts["user_settings"] == 0 || counts["vehicle_profiles"] == 0 || counts["version"] == 0 || counts["created_at"] == 0 {
		t.Errorf("envelope incomplete: %v", counts)
	}
	delta := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("streamed %d MB, peak heap delta %d MB", resp.n.Load()>>20, delta>>20)
	if resp.n.Load() < 20<<20 {
		t.Errorf("only %d MB streamed; the dataset is too small to prove anything", resp.n.Load()>>20)
	}
	// Streaming holds the heap flat (about 4 MB measured); loading every sample
	// first, as the old handler did, costs 30 MB or more for this dataset.
	if delta > 15<<20 {
		t.Errorf("heap grew by %d MB while streaming; want under 15", delta>>20)
	}

	// Second request inside the gap: 429 with Retry-After.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/data/backup", nil).WithContext(auth.WithUser(ctx, uid)))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("repeat = %d Retry-After %q, want 429 with a value", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// What the backup writes, restore reads back: a backup of one account restores
// into another with the same counts.
func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	src := seedBackupUser(t, ctx, pool, "bk-src", 5000, 40, 25)
	dst, err := db.EnsureUser(ctx, pool, "bk-dst")
	if err != nil {
		t.Fatal(err)
	}
	deps := backupTestDeps(pool)

	rec := httptest.NewRecorder()
	handleDataBackup(deps)(rec, httptest.NewRequest("GET", "/api/data/backup", nil).WithContext(auth.WithUser(ctx, src)))
	if rec.Code != 200 {
		t.Fatalf("backup = %d", rec.Code)
	}
	// Gzip by default, served as a download named .json.gz.
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="rivolt-backup-`) || !strings.HasSuffix(cd, `.json.gz"`) || rec.Header().Get("Content-Type") != "application/gzip" {
		t.Errorf("Content-Disposition = %q, Content-Type = %q", cd, rec.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("backup is not gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || !json.Valid(plain) {
		t.Fatalf("backup does not unpack to valid JSON: %v", err)
	}
	t.Logf("gzip %d KB -> %d KB", rec.Body.Len()>>10, len(plain)>>10)

	// Restore it into another account (its own vehicle id is created on demand).
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-bk-src','R1S')`, dst); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handleDataRestore(deps)(rr, httptest.NewRequest("POST", "/api/data/restore", bytes.NewReader(rec.Body.Bytes())).WithContext(auth.WithUser(ctx, dst)))
	ev, _, got := final(t, rr)
	if ev != "done" || got.Samples != 5000 || got.Drives != 40 || got.Charges != 25 {
		t.Fatalf("restore = %s %+v", ev, got)
	}
	for table, want := range map[string]int{"vehicle_state": 5000, "drives": 40, "charges": 25} {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE user_id = $1`, dst).Scan(&n); err != nil || n != want {
			t.Errorf("%s restored = %d (%v), want %d", table, n, err, want)
		}
	}
}

// A client that goes away stops the backup instead of reading on.
func TestBackupStopsWhenClientDisconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid := seedBackupUser(t, ctx, pool, "bk-cancel", 100_000, 10, 10)
	reqCtx, cancelReq := context.WithCancel(auth.WithUser(ctx, uid))
	pr, pw := io.Pipe()
	resp := &pipeResponse{h: http.Header{}, pw: pw}
	finished := make(chan any, 1)
	go func() {
		defer func() { finished <- recover(); pw.Close() }()
		handleDataBackup(backupTestDeps(pool))(resp, httptest.NewRequest("GET", "/api/data/backup", nil).WithContext(reqCtx))
	}()
	buf := make([]byte, 1<<20)
	if _, err := io.ReadFull(pr, buf); err != nil { // take some of it, then hang up
		t.Fatal(err)
	}
	cancelReq()
	pr.Close()
	select {
	case r := <-finished:
		if r != http.ErrAbortHandler {
			t.Errorf("handler ended with %v, want the connection abort", r)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("backup kept running after the client hung up")
	}
}
