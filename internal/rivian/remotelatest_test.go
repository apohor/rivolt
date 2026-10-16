package rivian

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore counts GetState calls and can be told to fail.
type countingStore struct {
	*memLiveStateStore
	gets atomic.Int64
	err  error
}

func (c *countingStore) GetState(ctx context.Context, id string) (*State, error) {
	c.gets.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.memLiveStateStore.GetState(ctx, id)
}

func TestRemoteLatestIsCachedForTheTTL(t *testing.T) {
	store := &countingStore{memLiveStateStore: newMemLiveStateStore()}
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	m.remoteNow = func() time.Time { return now }
	ctx := context.Background()

	// A miss is not cached: a snapshot published a moment later is seen at once.
	if m.RemoteLatest(ctx, "v1") != nil {
		t.Fatal("unexpected snapshot")
	}
	if err := store.PutState(ctx, "v1", &State{VehicleID: "v1", BatteryLevelPct: 61}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if st := m.RemoteLatest(ctx, "v1"); st == nil || st.BatteryLevelPct != 61 {
		t.Fatalf("snapshot not seen after publish: %+v", st)
	}
	before := store.gets.Load()
	for i := 0; i < 50; i++ {
		if st := m.RemoteLatest(ctx, "v1"); st == nil || st.BatteryLevelPct != 61 {
			t.Fatalf("cached read %d: %+v", i, st)
		}
	}
	if store.gets.Load() != before {
		t.Errorf("50 reads inside the TTL made %d store calls", store.gets.Load()-before)
	}
	// Per vehicle: another vehicle is its own read.
	m.RemoteLatest(ctx, "v2")
	if store.gets.Load() != before+1 {
		t.Errorf("another vehicle shared the cache entry")
	}
	// After the TTL the owner's newer snapshot is read.
	_ = store.PutState(ctx, "v1", &State{VehicleID: "v1", BatteryLevelPct: 62}, time.Minute)
	now = now.Add(remoteLatestTTL + time.Millisecond)
	if st := m.RemoteLatest(ctx, "v1"); st == nil || st.BatteryLevelPct != 62 {
		t.Errorf("stale snapshot after the TTL: %+v", st)
	}
}

func TestRemoteLatestErrorsAreNotCached(t *testing.T) {
	store := &countingStore{memLiveStateStore: newMemLiveStateStore()}
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	ctx := context.Background()
	store.err = errors.New("redis down")
	if m.RemoteLatest(ctx, "v1") != nil {
		t.Fatal("error returned a state")
	}
	store.err = nil
	_ = store.PutState(ctx, "v1", &State{VehicleID: "v1", BatteryLevelPct: 5}, time.Minute)
	if st := m.RemoteLatest(ctx, "v1"); st == nil {
		t.Fatal("recovered store not read: the error was cached")
	}
}

func TestRemoteLatestCacheIsBounded(t *testing.T) {
	store := &countingStore{memLiveStateStore: newMemLiveStateStore()}
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	m.remoteNow = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < remoteCacheMax+50; i++ {
		id := "v" + time.Duration(i).String()
		_ = store.PutState(ctx, id, &State{VehicleID: id}, time.Minute)
		m.RemoteLatest(ctx, id)
	}
	m.remoteMu.Lock()
	n := len(m.remoteCache)
	m.remoteMu.Unlock()
	if n > remoteCacheMax {
		t.Errorf("cache grew to %d (max %d)", n, remoteCacheMax)
	}
}
