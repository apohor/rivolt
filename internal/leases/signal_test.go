package leases

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// A bump reaches the other pods within a poll and not the pod that made it.
func TestRedisSignalReachesPeersNotItself(t *testing.T) {
	mr := miniredis.RunT(t)
	mk := func() *RedisSignal {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return NewRedisSignal(rdb, "rivolt")
	}
	a, b := mk(), mk()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotA, gotB atomic.Int32
	go a.Subscribe(ctx, func() { gotA.Add(1) })
	go b.Subscribe(ctx, func() { gotB.Add(1) })
	time.Sleep(200 * time.Millisecond) // both have read their baseline
	if err := a.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(signalPoll*2 + time.Second)
	for gotB.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if gotB.Load() != 1 {
		t.Fatalf("the peer heard %d signals, want 1", gotB.Load())
	}
	time.Sleep(signalPoll + 200*time.Millisecond)
	if gotA.Load() != 0 || gotB.Load() != 1 {
		t.Errorf("sender heard %d (want 0), peer %d (want 1)", gotA.Load(), gotB.Load())
	}
	if ttl := mr.TTL("rivolt:leases:released"); ttl <= 0 {
		t.Errorf("the counter has no expiry (%v)", ttl)
	}
}

// A Redis that is down is a skipped look, not a crash or a signal.
func TestRedisSignalSurvivesRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewRedisSignal(rdb, "rivolt")
	ctx, cancel := context.WithCancel(context.Background())
	var got atomic.Int32
	done := make(chan struct{})
	go func() { s.Subscribe(ctx, func() { got.Add(1) }); close(done) }()
	time.Sleep(200 * time.Millisecond)
	mr.Close()
	time.Sleep(signalPoll + 300*time.Millisecond)
	if err := s.Publish(ctx); err == nil {
		t.Error("Publish to a dead Redis should fail (best effort for the caller)")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not stop")
	}
	if got.Load() != 0 {
		t.Errorf("%d signals from a dead Redis", got.Load())
	}
}

type fakeSignal struct {
	mu   sync.Mutex
	sent int
	err  error
}

func (f *fakeSignal) Publish(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent++
	return f.err
}
func (f *fakeSignal) Subscribe(ctx context.Context, _ func()) { <-ctx.Done() }

// A burst of releases is one message; a failed send is not an error for the drain.
func TestSignalReleasedIsRateLimitedAndBestEffort(t *testing.T) {
	f := &fakeSignal{err: context.DeadlineExceeded}
	c := &Coordinator{log: slog.Default(), trigger: make(chan struct{}, 1)}
	c.SetReleaseSignal(f)
	for i := 0; i < 5; i++ {
		c.signalReleased(context.Background())
	}
	if f.sent != 1 {
		t.Errorf("%d messages for a burst of 5 releases, want 1", f.sent)
	}
	c.sigAt = time.Now().Add(-time.Second)
	c.signalReleased(context.Background())
	if f.sent != 2 {
		t.Errorf("%d messages after the gap, want 2", f.sent)
	}
	(&Coordinator{log: slog.Default()}).signalReleased(context.Background()) // no signal wired: nothing happens
}

// A peer reconciles soon after a signal, and a pod that is shutting down does not.
func TestPeerReconcilesOnASignalUnlessClosing(t *testing.T) {
	c := &Coordinator{log: slog.Default(), trigger: make(chan struct{}, 1)}
	c.onReleasedSignal()
	select {
	case <-c.trigger:
	case <-time.After(signalJitter + time.Second):
		t.Fatal("no reconcile after the signal")
	}
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	c.onReleasedSignal()
	select {
	case <-c.trigger:
		t.Fatal("a closing pod reconciled on a signal")
	case <-time.After(signalJitter + 200*time.Millisecond):
	}
}
