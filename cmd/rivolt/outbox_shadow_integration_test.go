//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/outbox"
	"github.com/apohor/rivolt/internal/testpg"
)

// The shadow close write: one close_hook row per session, a repeat is the same row, a lost lease writes nothing, and a database failure is
// counted and returned to nobody.
func TestOutboxShadowRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	uid, err := db.EnsureUser(ctx, pool, "shadow-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	rv := "rv-" + uuid.NewString()
	o := newOutboxShadow(pool, outbox.NewStore(pool, nil), db.NewVehicleResolverFactory(pool), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	count := func() (n int) {
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE user_id = $1 AND kind = 'close_hook'`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	o.record(ctx, uid, rv, "drive", "d", "drive-1")
	o.record(ctx, uid, rv, "drive", "d", "drive-1") // a repeated close is the same intent
	if got := count(); got != 1 {
		t.Fatalf("after a drive closed twice: %d rows, want 1", got)
	}
	o.record(ctx, uid, rv, "charge", "c", "drive-1") // same id, other type: its own row
	if got := count(); got != 2 {
		t.Fatalf("after a charge close: %d rows, want 2", got)
	}
	var payload string
	if err := pool.QueryRowContext(ctx, `SELECT payload::text FROM outbox WHERE key LIKE '%:d:drive-1'`).Scan(&payload); err != nil || payload != `{"type": "drive", "session": "drive-1"}` && payload != `{"session": "drive-1", "type": "drive"}` {
		t.Fatalf("payload = %q, %v", payload, err)
	}

	// A token for a lease nobody holds: the insert is skipped, the way the recorder's own writes are.
	lost := fence.With(ctx, fence.Token{Group: uuid.New().String(), Pod: "other", Epoch: 7})
	o.record(lost, uid, rv, "drive", "d", "drive-2")
	if got := count(); got != 2 {
		t.Fatalf("a lost lease wrote a row: %d rows, want 2", got)
	}

	// A broken database is counted and logged; the hook has nothing to return.
	_ = pool.Close()
	o.record(ctx, uid, rv, "drive", "d", "drive-3")
	if got := testutil.ToFloat64(o.errs.WithLabelValues("drive")); got != 1 {
		t.Fatalf("errors = %v, want 1", got)
	}
}

func TestOutboxShadowFlag(t *testing.T) {
	t.Setenv("RIVOLT_OUTBOX_SHADOW", "")
	if outboxShadowOn() {
		t.Fatal("shadow writes are on by default")
	}
	t.Setenv("RIVOLT_OUTBOX_SHADOW", "1")
	if !outboxShadowOn() {
		t.Fatal("RIVOLT_OUTBOX_SHADOW=1 does not turn them on")
	}
}
