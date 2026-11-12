//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/outbox"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// Shadow parity at volume (Sprint 19 8, slice 2): preview sees about three close hooks a day, too few to judge. The generated sample truck's
// whole loop goes through the real recorder lifecycle (rivian.Replayer), and the shadow hooks built by the same functions the registry uses
// are added next to the direct ones. Then: close hooks fired (rivolt_close_hooks_fired_total's observer) equals close_hook rows inserted
// (rivolt_outbox_inserted_total), per type, with 0 shadow errors, and the count is large enough that equality means something.
func TestOutboxShadowParityOverTheSampleLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "parity-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	const rv = "rv-parity-1"
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`, uid, rv); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	store := outbox.NewStore(pool, reg)
	shadow := newOutboxShadow(pool, store, db.NewVehicleResolverFactory(pool), slog.New(slog.NewTextHandler(io.Discard, nil)), reg)

	var fired = map[string]*atomic.Int64{"drive": {}, "charge": {}}
	rivian.SetCloseHookObserver(func(typ string) { fired[typ].Add(1) })
	t.Cleanup(func() { rivian.SetCloseHookObserver(func(string) {}) })

	frames, err := rivian.NewSampleCorpus(1).Frames(ctx, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(frames) == 0 {
		t.Fatalf("sample frames: %d, %v", len(frames), err)
	}
	r := rivian.NewReplayer(rv)
	r.AlsoOnDriveClose(shadow.driveHook(uid))
	r.AlsoOnChargeClose(shadow.chargeHook(uid))
	r.FeedAll(frames)
	r.Drives()
	r.Charges() // the hooks run in goroutines; both settle waits return once no new session closes

	var drivesIn, chargesIn int
	// The shadow insert runs after the collectors in the same goroutine: wait until the rows stop arriving.
	for last, stable := -1, 0; stable < 5; time.Sleep(200 * time.Millisecond) {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE key LIKE '%:d:%'), count(*) FILTER (WHERE key LIKE '%:c:%') FROM outbox WHERE user_id = $1 AND kind = 'close_hook'`, uid).Scan(&drivesIn, &chargesIn); err != nil {
			t.Fatal(err)
		}
		if n := drivesIn + chargesIn; n == last {
			stable++
		} else {
			last, stable = n, 0
		}
	}

	firedDrive, firedCharge := int(fired["drive"].Load()), int(fired["charge"].Load())
	t.Logf("fired: drive %d charge %d; inserted: drive %d charge %d; frames %d", firedDrive, firedCharge, drivesIn, chargesIn, len(frames))
	if firedDrive < 20 || firedCharge < 10 {
		t.Fatalf("the loop fired %d drive and %d charge hooks: too few for parity to mean anything", firedDrive, firedCharge)
	}
	if firedDrive != drivesIn || firedCharge != chargesIn {
		t.Errorf("fired drive %d charge %d, inserted drive %d charge %d: not equal", firedDrive, firedCharge, drivesIn, chargesIn)
	}
	if got := gathered(t, reg, "rivolt_outbox_inserted_total", "kind", "close_hook"); int(got) != drivesIn+chargesIn {
		t.Errorf("rivolt_outbox_inserted_total{kind=close_hook} = %v, rows = %d", got, drivesIn+chargesIn)
	}
	for _, typ := range []string{"drive", "charge"} {
		if got := testutil.ToFloat64(shadow.errs.WithLabelValues(typ)); got != 0 {
			t.Errorf("shadow errors{%s} = %v, want 0", typ, got)
		}
	}
}

// gathered reads one counter series from reg.
func gathered(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("%s{%s=%q} not found", name, label, value)
	return 0
}
