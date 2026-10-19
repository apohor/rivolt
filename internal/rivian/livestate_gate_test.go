package rivian

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore counts calls and can fail them.
type countingStore struct {
	*memLiveStateStore
	calls atomic.Int32
	err   error
}

func (c *countingStore) Save(ctx context.Context, v string, s LiveStateSnapshot, ttl time.Duration) error {
	c.calls.Add(1)
	if c.err != nil {
		return c.err
	}
	return c.memLiveStateStore.Save(ctx, v, s, ttl)
}

func (c *countingStore) Load(ctx context.Context, v string) (LiveStateSnapshot, bool, error) {
	c.calls.Add(1)
	if c.err != nil {
		return LiveStateSnapshot{}, false, c.err
	}
	return c.memLiveStateStore.Load(ctx, v)
}

// Until Redis is ready the gated store never touches it and answers "nothing
// stored"; once ready, the same store (already held by running monitors) starts
// persisting and rehydrating.
func TestGatedLiveStateStoreStartsWorkingWhenRedisIsReady(t *testing.T) {
	inner := &countingStore{memLiveStateStore: newMemLiveStateStore()}
	var up atomic.Bool
	g := NewGatedLiveStateStore(inner, up.Load)
	ctx := context.Background()
	snap := LiveStateSnapshot{DriveCounter: 7}

	if err := g.Save(ctx, "v1", snap, 0); err != nil {
		t.Fatalf("Save before ready: %v", err)
	}
	if _, ok, err := g.Load(ctx, "v1"); ok || err != nil {
		t.Fatalf("Load before ready: ok=%v err=%v, want not found", ok, err)
	}
	if inner.calls.Load() != 0 {
		t.Fatalf("Redis was touched %d times before it was ready", inner.calls.Load())
	}

	up.Store(true)
	if err := g.Save(ctx, "v1", snap, 0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := g.Load(ctx, "v1")
	if err != nil || !ok || got.DriveCounter != 7 {
		t.Fatalf("Load after ready: %+v ok=%v err=%v", got, ok, err)
	}
}

// A failing Redis costs one call, then is left alone for the backoff.
func TestGatedLiveStateStoreBacksOffAfterAFailure(t *testing.T) {
	boom := errors.New("connection refused")
	inner := &countingStore{memLiveStateStore: newMemLiveStateStore(), err: boom}
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	g := NewGatedLiveStateStore(inner, nil)
	g.now = func() time.Time { return now }
	ctx := context.Background()

	if err := g.Save(ctx, "v1", LiveStateSnapshot{}, 0); !errors.Is(err, boom) {
		t.Fatalf("first failure = %v, want the error", err)
	}
	for i := 0; i < 5; i++ {
		if err := g.Save(ctx, "v1", LiveStateSnapshot{}, 0); err != nil {
			t.Fatalf("call %d during the backoff = %v, want a quiet no-op", i, err)
		}
		if _, ok, err := g.Load(ctx, "v1"); ok || err != nil {
			t.Fatalf("Load during the backoff: ok=%v err=%v", ok, err)
		}
	}
	if inner.calls.Load() != 1 {
		t.Errorf("Redis was called %d times, want 1 (then backoff)", inner.calls.Load())
	}
	now = now.Add(gatedBackoff + time.Second)
	inner.err = nil
	if err := g.Save(ctx, "v1", LiveStateSnapshot{DriveCounter: 3}, 0); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := g.Load(ctx, "v1"); !ok || got.DriveCounter != 3 {
		t.Errorf("did not recover after the backoff: %+v ok=%v", got, ok)
	}
}

// The context handed to Redis carries the short timeout, not the caller's none.
func TestGatedLiveStateStoreBoundsEachCall(t *testing.T) {
	var deadline time.Time
	var has bool
	inner := &deadlineStore{memLiveStateStore: newMemLiveStateStore(), seen: func(d time.Time, ok bool) { deadline, has = d, ok }}
	g := NewGatedLiveStateStore(inner, nil)
	_ = g.Delete(context.Background(), "v1")
	if !has || time.Until(deadline) > gatedOpTimeout+50*time.Millisecond {
		t.Errorf("Redis call deadline = %v (set %v), want within %v", time.Until(deadline), has, gatedOpTimeout)
	}
}

type deadlineStore struct {
	*memLiveStateStore
	seen func(time.Time, bool)
}

func (d *deadlineStore) Delete(ctx context.Context, v string) error {
	dl, ok := ctx.Deadline()
	d.seen(dl, ok)
	return d.memLiveStateStore.Delete(ctx, v)
}
