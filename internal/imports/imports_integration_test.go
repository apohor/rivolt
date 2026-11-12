//go:build integration

package imports_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/imports"
	"github.com/apohor/rivolt/internal/testpg"
)

const vin = "IMPORTVIN0000001"

type env struct {
	pool *sql.DB
	uid  uuid.UUID
	ds   *drives.Store
	cs   *charges.Store
	is   *imports.Store
}

func setup(t *testing.T) env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "imports-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	is, err := imports.New(pool, uid)
	if err != nil {
		t.Fatal(err)
	}
	r := db.NewVehicleResolverFactory(pool)
	return env{pool, uid, drives.NewFactory(pool, r).For(uid), charges.NewFactory(pool, r).For(uid), is}
}

func (e env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sha(c string) string { return strings.Repeat(c, 64) }

func drive(id string, start time.Time, d time.Duration) drives.Drive {
	return drives.Drive{ID: id, VehicleID: vin, StartedAt: start, EndedAt: start.Add(d), DistanceMi: 5, Source: "tezlab_import"}
}

func charge(id string, start time.Time, d time.Duration) charges.Charge {
	return charges.Charge{ID: id, VehicleID: vin, StartedAt: start, EndedAt: start.Add(d), StartSoCPct: 20, EndSoCPct: 60, EnergyAddedKWh: 30, FinalState: "complete", Source: "tezlab_import"}
}

func TestBeginRefusesLiveDuplicateAndAllowsAfterUndo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	b := imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("a"), Bytes: 10}
	id, err := e.is.Begin(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	var fn string
	if err := e.pool.QueryRow(`SELECT filename FROM imports WHERE id = $1`, id).Scan(&fn); err != nil || fn != "tezlab_import.csv" {
		t.Fatalf("filename = %q, %v", fn, err)
	}
	var dup *imports.AlreadyImported
	if _, err := e.is.Begin(ctx, b); !errors.As(err, &dup) || dup.ID != id {
		t.Fatalf("second Begin = %v, want AlreadyImported for %s", err, id)
	}
	if _, err := e.is.Undo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.is.Begin(ctx, b); err != nil {
		t.Fatalf("Begin after undo: %v", err)
	}
	if _, err := e.is.Undo(ctx, id); !errors.Is(err, imports.ErrNotFound) {
		t.Fatalf("second Undo = %v, want ErrNotFound", err)
	}
}

func TestConcurrentBeginGivesOneBatch(t *testing.T) {
	e := setup(t)
	b := imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("b"), Bytes: 1}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, dup := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.is.Begin(context.Background(), b)
			var d *imports.AlreadyImported
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &d):
				dup++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || dup != 7 {
		t.Fatalf("ok=%d dup=%d, want 1 and 7", ok, dup)
	}
	if n := e.count(t, `SELECT count(*) FROM imports WHERE user_id = $1`, e.uid); n != 1 {
		t.Fatalf("%d batches", n)
	}
}

func TestInsertImportedIsInsertOnlyAndUndoRemovesEverything(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, err := e.is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("c"), Bytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	if ok, err := e.ds.InsertImported(ctx, id, drive("imp-d1", t0, time.Hour), nil); err != nil || !ok {
		t.Fatalf("insert drive: %v %v", ok, err)
	}
	if ok, err := e.cs.InsertImported(ctx, id, charge("imp-c1", t0.Add(3*time.Hour), time.Hour), nil); err != nil || !ok {
		t.Fatalf("insert charge: %v %v", ok, err)
	}
	// A user edit survives a re-insert of the same external id.
	if _, err := e.pool.Exec(`UPDATE drives SET metadata = metadata || '{"notes":"mine"}' WHERE user_id = $1 AND external_id = 'imp-d1'`, e.uid); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.ds.InsertImported(ctx, id, drive("imp-d1", t0, time.Hour), nil); err != nil || ok {
		t.Fatalf("re-insert drive: %v %v", ok, err)
	}
	if n := e.count(t, `SELECT count(*) FROM drives WHERE user_id = $1 AND metadata->>'notes' = 'mine'`, e.uid); n != 1 {
		t.Fatal("edited row was overwritten")
	}
	if ok, err := e.cs.InsertImported(ctx, id, charge("imp-c1", t0.Add(3*time.Hour), time.Hour), nil); err != nil || ok {
		t.Fatalf("re-insert charge: %v %v", ok, err)
	}
	for _, q := range []string{
		`INSERT INTO drive_recaps (user_id, drive_id, model, recap) VALUES ($1, 'imp-d1', 'm', 'r')`,
		`INSERT INTO drive_efficiency (user_id, drive_id, model, analysis_text, result_json) VALUES ($1, 'imp-d1', 'm', 'a', '{}')`,
		`INSERT INTO drive_weather (user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider) VALUES ($1, 'imp-d1', 1, 1, now(), 'p')`,
		`INSERT INTO drive_weather_series (user_id, drive_id, sampled_at, cadence_minutes, coarse_lat, coarse_lon, provider) VALUES ($1, 'imp-d1', now(), 15, 1, 1, 'p')`,
	} {
		if _, err := e.pool.Exec(q, e.uid); err != nil {
			t.Fatalf("seed per-drive row: %v", err)
		}
	}
	// A live drive of the same user is not touched.
	if err := e.ds.Upsert(ctx, drive("live-1", t0.Add(48*time.Hour), time.Hour)); err != nil {
		t.Fatal(err)
	}
	u, err := e.is.Undo(ctx, id)
	if err != nil || u.Drives != 1 || u.Charges != 1 {
		t.Fatalf("Undo = %+v, %v", u, err)
	}
	if n := e.count(t, `SELECT (SELECT count(*) FROM drives WHERE import_id = $1) + (SELECT count(*) FROM charges WHERE import_id = $1)`, id); n != 0 {
		t.Fatalf("%d rows keep import_id", n)
	}
	for _, tb := range []string{"drive_recaps", "drive_efficiency", "drive_weather", "drive_weather_series"} {
		if n := e.count(t, `SELECT count(*) FROM `+tb+` WHERE user_id = $1 AND drive_id = 'imp-d1'`, e.uid); n != 0 {
			t.Fatalf("%s keeps %d rows", tb, n)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM drives WHERE user_id = $1 AND external_id = 'live-1'`, e.uid); n != 1 {
		t.Fatal("undo removed a live drive")
	}
	if n := e.count(t, `SELECT count(*) FROM imports WHERE id = $1 AND undone_at IS NOT NULL`, id); n != 1 {
		t.Fatal("batch not marked undone")
	}
}

func TestCoversRule(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, _ := e.is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("d"), Bytes: 1})
	t0 := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	if _, err := e.ds.InsertImported(ctx, id, drive("cov-1", t0, time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		start, end time.Time
		want       bool
	}{
		{"same session", t0, t0.Add(time.Hour), true},
		{"half of the shorter", t0.Add(45 * time.Minute), t0.Add(75 * time.Minute), true},
		{"starts within 5 min and overlaps a little", t0.Add(-4 * time.Minute), t0.Add(time.Minute), true},
		{"starts 6 min earlier, overlaps a little", t0.Add(-6 * time.Minute), t0.Add(time.Minute), false},
		{"short overlap, starts later", t0.Add(50 * time.Minute), t0.Add(3 * time.Hour), false},
		{"touching but not overlapping", t0.Add(time.Hour), t0.Add(2 * time.Hour), false},
		{"far away", t0.Add(24 * time.Hour), t0.Add(25 * time.Hour), false},
		{"zero length", t0.Add(10 * time.Minute), t0.Add(10 * time.Minute), false},
	}
	for _, c := range cases {
		got, err := e.ds.Covers(ctx, vin, c.start, c.end)
		if err != nil || got != c.want {
			t.Errorf("drive %s: got %v (%v), want %v", c.name, got, err, c.want)
		}
	}
	if _, err := e.cs.InsertImported(ctx, id, charge("cov-c1", t0, time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := e.cs.Covers(ctx, vin, t0.Add(10*time.Minute), t0.Add(50*time.Minute)); err != nil || !got {
		t.Errorf("charge inside: %v %v", got, err)
	}
	if got, err := e.cs.Covers(ctx, vin, t0.Add(24*time.Hour), t0.Add(25*time.Hour)); err != nil || got {
		t.Errorf("charge far: %v %v", got, err)
	}
}

func TestImportFKsAreValidated(t *testing.T) {
	e := setup(t)
	if n := e.count(t, `SELECT count(*) FROM pg_constraint WHERE conname IN ('drives_import_id_fkey','charges_import_id_fkey') AND convalidated`); n != 2 {
		t.Fatalf("%d of 2 import FKs validated", n)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_indexes WHERE indexname = 'imports_user_sha'`); n != 0 {
		t.Fatal("old imports_user_sha index still present")
	}
}

func TestUndoRacingInsertsLeavesNothingBehind(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, err := e.is.Begin(ctx, imports.Batch{Source: "tezlab_import", Ext: "csv", SHA256: sha("e"), Bytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			at := t0.Add(time.Duration(i) * time.Hour)
			if _, err := e.ds.InsertImported(ctx, id, drive("race-d"+strings.Repeat("x", i%3)+time.Duration(i).String(), at, 20*time.Minute), nil); err != nil && !errors.Is(err, drives.ErrImportClosed) {
				t.Error(err)
				return
			}
			if _, err := e.cs.InsertImported(ctx, id, charge("race-c"+time.Duration(i).String(), at, 20*time.Minute), nil); err != nil && !errors.Is(err, charges.ErrImportClosed) {
				t.Error(err)
				return
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)
	if _, err := e.is.Undo(ctx, id); err != nil {
		t.Fatal(err)
	}
	<-done
	if n := e.count(t, `SELECT (SELECT count(*) FROM drives WHERE import_id = $1) + (SELECT count(*) FROM charges WHERE import_id = $1)`, id); n != 0 {
		t.Fatalf("%d rows carry the undone import id", n)
	}
	if _, err := e.ds.InsertImported(ctx, id, drive("late", t0, time.Hour), nil); !errors.Is(err, drives.ErrImportClosed) {
		t.Fatalf("insert after undo = %v, want ErrImportClosed", err)
	}
}
