package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Plain and gzip bodies both come out as the same text; gzip is recognised
// by its magic bytes, not by a header the client may or may not send.
func TestRestoreSourceDetectsGzip(t *testing.T) {
	const doc = `{"drives":[],"charges":[],"samples":[]}`
	for name, body := range map[string][]byte{"plain": []byte(doc), "gzip": gz(t, doc)} {
		src, closeFn, err := restoreSource(bytes.NewReader(body), 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(src)
		closeFn()
		if err != nil || string(got) != doc {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	// Corrupt gzip (magic bytes, garbage after) is an error, not a hang.
	if src, _, err := restoreSource(bytes.NewReader([]byte{0x1f, 0x8b, 0, 1, 2}), 1<<20); err == nil {
		if _, err := io.ReadAll(src); err == nil {
			t.Error("corrupt gzip read without error")
		}
	}
}

// The cap counts decompressed bytes, so a small gzip that unpacks huge is stopped.
func TestRestoreSourceCapsDecompressedSize(t *testing.T) {
	bomb := gz(t, strings.Repeat("a", 1<<20)) // ~1 KB on the wire, 1 MiB unpacked
	if len(bomb) > 4<<10 {
		t.Fatalf("test input is not small: %d bytes", len(bomb))
	}
	src, closeFn, err := restoreSource(bytes.NewReader(bomb), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := io.ReadAll(src); !errors.Is(err, errRestoreTooBig) {
		t.Errorf("read error = %v, want errRestoreTooBig", err)
	}
}

func TestRestoreGateTimeBounds(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g := newRestoreGate(now)
	ok := []time.Time{
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), // first accepted instant
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		now.Add(23 * time.Hour), // clock skew allowance
	}
	for _, ts := range ok {
		if !g.timesOK(ts) {
			t.Errorf("%v rejected", ts)
		}
	}
	bad := []time.Time{
		time.Date(2020, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Time{}, // zero time
		time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		now.Add(25 * time.Hour),
		time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for _, ts := range bad {
		if g.timesOK(ts) {
			t.Errorf("%v accepted", ts)
		}
	}
	if g.skipped.Load() != int64(len(bad)) {
		t.Errorf("skipped = %d, want %d", g.skipped.Load(), len(bad))
	}
	// A drive is skipped if either end is out of range.
	if g.timesOK(ok[1], bad[0]) {
		t.Error("a row with one bad time was accepted")
	}
}

func TestRestoreGateVehicleCap(t *testing.T) {
	g := newRestoreGate(time.Now())
	g.owned["mine"] = true
	if err := g.vehicleOK("mine"); err != nil {
		t.Fatalf("owned vehicle: %v", err)
	}
	for i := 0; i < restoreMaxNewVehicles; i++ {
		if err := g.vehicleOK(string(rune('a' + i))); err != nil {
			t.Fatalf("new vehicle %d: %v", i, err)
		}
	}
	// The same new ids again, and owned ones, are always fine.
	if err := g.vehicleOK("a"); err != nil {
		t.Errorf("repeat of a new id: %v", err)
	}
	if err := g.vehicleOK("mine"); err != nil {
		t.Errorf("owned after the cap: %v", err)
	}
	if err := g.vehicleOK("one-too-many"); !errors.Is(err, errRestoreVehicles) {
		t.Errorf("over the cap: %v, want errRestoreVehicles", err)
	}
}

// Unexpected failures never put internal text (SQL state, table or constraint
// names, ids) in the event the client reads.
func TestRestoreErrorEventHidesInternals(t *testing.T) {
	internal := errors.New(`insert samples: ERROR: duplicate key value violates unique constraint "vehicle_state_y2026_03_pkey" (SQLSTATE 23505)`)
	st, msg := restoreErrorEvent(fmt.Errorf("upsert drive d-1: %w", internal))
	if st != 500 || strings.Contains(msg, "SQLSTATE") || strings.Contains(msg, "vehicle_state") || strings.Contains(msg, "d-1") || !strings.Contains(msg, "running it again is safe") {
		t.Errorf("500 event = %d %q", st, msg)
	}
	if st, msg := restoreErrorEvent(fmt.Errorf("%w: unexpected EOF", errRestoreBadFile)); st != 400 || !strings.Contains(msg, "unexpected EOF") {
		t.Errorf("bad-file event = %d %q", st, msg)
	}
	if st, _ := restoreErrorEvent(errRestoreTooBig); st != 413 {
		t.Errorf("too-big status = %d", st)
	}
	if st, msg := restoreErrorEvent(fmt.Errorf("%w (more than 5)", errRestoreVehicles)); st != 400 || !strings.Contains(msg, "vehicles") {
		t.Errorf("vehicle-cap event = %d %q", st, msg)
	}
}

// A network or deadline failure reading the body is not a malformed backup,
// and its text (which can carry internal addresses) never reaches the client.
func TestRestoreReadErrKeepsNetworkErrorsOutOfTheMessage(t *testing.T) {
	ctx := context.Background()
	netErr := &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.42.3.7"), Port: 8080}, Err: os.ErrDeadlineExceeded}
	for name, err := range map[string]error{
		"net error":      netErr,
		"deadline":       fmt.Errorf("read: %w", os.ErrDeadlineExceeded),
		"context cancel": context.Canceled,
	} {
		got := restoreReadErr(ctx, err)
		if errors.Is(got, errRestoreBadFile) {
			t.Errorf("%s was classified as a bad backup", name)
		}
		if st, msg := restoreErrorEvent(got); st != 500 || strings.Contains(msg, "10.42") || strings.Contains(msg, "tcp") {
			t.Errorf("%s event = %d %q", name, st, msg)
		}
	}
	// A malformed document still is one, with its parse text.
	var v any
	syntax := json.Unmarshal([]byte(`{"a":`), &v)
	if got := restoreReadErr(ctx, syntax); !errors.Is(got, errRestoreBadFile) {
		t.Errorf("json error = %v, want a bad-backup error", got)
	}
	if got := restoreReadErr(ctx, errRestoreTooBig); !errors.Is(got, errRestoreTooBig) {
		t.Errorf("size error lost its identity: %v", got)
	}
}
