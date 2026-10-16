package rivian

import (
	"context"
	"testing"
	"time"
)

type countingLiveStateStore struct {
	*memLiveStateStore
	gets int
}

func (s *countingLiveStateStore) GetState(ctx context.Context, vehicleID string) (*State, error) {
	s.gets++
	return s.memLiveStateStore.GetState(ctx, vehicleID)
}

// Polls within remoteLatestTTL share one store read; a miss isn't
// cached; each caller gets its own copy; after the TTL it reads again.
func TestRemoteLatestReusesARecentRead(t *testing.T) {
	ctx := context.Background()
	store := &countingLiveStateStore{memLiveStateStore: newMemLiveStateStore()}
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)

	if m.RemoteLatest(ctx, "v") != nil {
		t.Fatal("nothing published yet")
	}
	_ = store.PutState(ctx, "v", &State{VehicleID: "v", BatteryLevelPct: 50}, LiveStateSnapshotTTL)
	a := m.RemoteLatest(ctx, "v")
	if a == nil || a.BatteryLevelPct != 50 {
		t.Fatalf("a miss was cached: %v", a)
	}
	a.BatteryLevelPct = 1 // a caller's copy is its own
	b := m.RemoteLatest(ctx, "v")
	if b == nil || b.BatteryLevelPct != 50 {
		t.Errorf("second read %v, want the cached 50", b)
	}
	if store.gets != 2 {
		t.Errorf("%d store reads for a miss and two polls, want 2", store.gets)
	}

	old := remoteLatestTTL
	remoteLatestTTL = time.Millisecond
	t.Cleanup(func() { remoteLatestTTL = old })
	_ = store.PutState(ctx, "v", &State{VehicleID: "v", BatteryLevelPct: 51}, LiveStateSnapshotTTL)
	time.Sleep(2 * time.Millisecond)
	if c := m.RemoteLatest(ctx, "v"); c == nil || c.BatteryLevelPct != 51 || store.gets != 3 {
		t.Errorf("after the TTL: %v with %d reads, want 51 with 3", c, store.gets)
	}
}
