package rivian

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
)

func countFailures(t *testing.T) (*int, func()) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	RecorderWriteFailureObserver = func(string) { mu.Lock(); n++; mu.Unlock() }
	return &n, func() { RecorderWriteFailureObserver = nil }
}

// Failed live writes are counted every time and logged at Warn, but only once a
// minute per kind and vehicle so an outage does not flood the log.
func TestNoteWriteFailureWarnsOncePerMinuteAndCountsAll(t *testing.T) {
	var buf bytes.Buffer
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	n, restore := countFailures(t)
	defer restore()

	boom := errors.New("connection refused")
	for i := 0; i < 5; i++ {
		m.noteWriteFailure("drive_upsert", "veh-1", "d1", boom)
	}
	m.noteWriteFailure("drive_upsert", "veh-2", "d2", boom)
	m.noteWriteFailure("charge_upsert", "veh-1", "c1", boom)
	if *n != 7 {
		t.Errorf("counted %d failures, want 7", *n)
	}
	var warns int
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "live write failed") {
			warns++
		}
	}
	if warns != 3 {
		t.Errorf("%d Warn lines, want 3 (one per kind and vehicle):\n%s", warns, buf.String())
	}
	// A minute later the same pair is loud again.
	m.writeFails.mu.Lock()
	m.writeFails.last["drive_upsert|veh-1"] = time.Now().Add(-2 * time.Minute)
	m.writeFails.mu.Unlock()
	buf.Reset()
	m.noteWriteFailure("drive_upsert", "veh-1", "d1", boom)
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("not loud again after a minute: %s", buf.String())
	}
}

// A drive upsert that hits a dead database is a Warn and a count, not a Debug
// line nobody reads.
func TestLiveDriveUpsertFailureIsWarnedAndCounted(t *testing.T) {
	var buf bytes.Buffer
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	n, restore := countFailures(t)
	defer restore()

	dead, err := sql.Open("pgx", "postgres://nobody:x@127.0.0.1:1/none?connect_timeout=1&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	uid := uuid.New()
	store, err := drives.OpenStore(dead, uid, db.NewVehicleResolverFactory(dead).For(uid))
	if err != nil {
		t.Fatal(err)
	}
	m.SetStores(nil, store, nil)

	t0 := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	d := &liveDrive{id: "live_v_d_1", number: 1, startedAt: t0, endAt: t0.Add(5 * time.Minute), startSoC: 80, endSoC: 78, startOdoMi: 100, endOdoMi: 103}
	m.upsertLiveDrive(context.Background(), "veh-1", d)

	if *n != 1 {
		t.Errorf("counted %d failures, want 1", *n)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "drive_upsert") {
		t.Errorf("no Warn for the failed drive upsert at Info level:\n%s", buf.String())
	}
}
