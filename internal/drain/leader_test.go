package drain

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeSession struct {
	mu        sync.Mutex
	err       error
	computes  int
	released  int
	discarded bool
}

func (f *fakeSession) compute(context.Context, time.Time) ([]Stat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.computes++
	if f.err != nil {
		return nil, f.err
	}
	return []Stat{{VehicleID: "rv", Window: Window24h, ParkedHours: 1}}, nil
}

func (f *fakeSession) release(discard bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released++
	f.discarded = discard
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A replica that does not hold the lock exports no drain series: a snapshot
// it computed while leader is cleared, so it can't keep serving stale numbers.
func TestNonLeaderClearsItsSnapshot(t *testing.T) {
	c := NewCollector()
	c.Set([]Stat{{VehicleID: "stale", Window: Window7d}}, time.Now().Add(-time.Hour), time.Second)
	c.elect = func(context.Context) (session, error) { return nil, nil } // the lock is taken elsewhere
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, nil, 10*time.Millisecond, quiet()); close(done) }()
	waitFor(t, "the snapshot to clear", func() bool { return c.snap.Load() == nil })
	cancel()
	<-done
}

// The leader computes on its own session, releases it cleanly on shutdown, and
// after a compute error drops the session (freeing the lock) and elects again.
func TestLeaderComputesReleasesAndReelectsAfterAnError(t *testing.T) {
	c := NewCollector()
	first := &fakeSession{}
	second := &fakeSession{}
	var mu sync.Mutex
	elections := 0
	c.elect = func(context.Context) (session, error) {
		mu.Lock()
		defer mu.Unlock()
		elections++
		if elections == 1 {
			return first, nil
		}
		return second, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, nil, 10*time.Millisecond, quiet()); close(done) }()
	waitFor(t, "the leader's snapshot", func() bool { return c.snap.Load() != nil })

	first.mu.Lock()
	first.err = errors.New("connection reset")
	first.mu.Unlock()
	waitFor(t, "a second election after the error", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return elections >= 2
	})
	first.mu.Lock()
	if first.released != 1 || !first.discarded {
		t.Errorf("failed session: released %d, discarded %v; want released once and discarded", first.released, first.discarded)
	}
	first.mu.Unlock()

	waitFor(t, "the new leader to compute", func() bool { second.mu.Lock(); defer second.mu.Unlock(); return second.computes > 0 })
	cancel()
	<-done
	second.mu.Lock()
	defer second.mu.Unlock()
	if second.released != 1 || second.discarded {
		t.Errorf("shutdown: released %d, discarded %v; want one clean release", second.released, second.discarded)
	}
}
