package rivian

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// VehicleStateFrameAt: the owner reports its feed's last frame unless the
// feed is held while asleep; a peer reads the owner's published snapshot.
func TestVehicleStateFrameAt(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	owner := NewStateMonitor(nil, nil)
	owner.SetLiveStateStore(store)
	owner.active["v"] = func() {}

	if got := owner.VehicleStateFrameAt(ctx, "v"); got != nil {
		t.Errorf("no frame yet: %v, want nil", got)
	}
	owner.mu.Lock()
	owner.noteVehicleStateFrameLocked("v")
	frame := owner.lastVSFrame["v"]
	owner.mu.Unlock()
	if got := owner.VehicleStateFrameAt(ctx, "v"); got == nil || !got.Equal(frame) {
		t.Errorf("owner: %v, want %v", got, frame)
	}
	owner.mu.Lock()
	owner.holdingVS = map[string]bool{"v": true}
	owner.mu.Unlock()
	if got := owner.VehicleStateFrameAt(ctx, "v"); got != nil {
		t.Errorf("held while asleep: %v, want nil", got)
	}
	owner.mu.Lock()
	delete(owner.holdingVS, "v")
	owner.mu.Unlock()

	// What liveStatePublisher puts in the store: the cache plus the frame
	// time. It survives the JSON the Redis store uses.
	owner.mu.RLock()
	snap := State{VehicleID: "v", PowerState: "sleep", VehicleStateFrameAt: owner.vehicleStateFrameAtLocked("v")}
	owner.mu.RUnlock()
	raw, _ := json.Marshal(snap)
	var back State
	if err := json.Unmarshal(raw, &back); err != nil || back.VehicleStateFrameAt == nil || !back.VehicleStateFrameAt.Equal(frame) {
		t.Fatalf("JSON round trip: %s → %v (%v)", raw, back.VehicleStateFrameAt, err)
	}
	_ = store.PutState(ctx, "v", &back, LiveStateSnapshotTTL)

	peer := NewStateMonitor(nil, nil)
	if got := peer.VehicleStateFrameAt(ctx, "v"); got != nil {
		t.Errorf("peer without a store: %v, want nil", got)
	}
	peer.SetLiveStateStore(store)
	if got := peer.VehicleStateFrameAt(ctx, "v"); got == nil || !got.Equal(frame) {
		t.Errorf("peer: %v, want the owner's %v", got, frame)
	}
	var nilMon *StateMonitor
	if nilMon.VehicleStateFrameAt(ctx, "v") != nil {
		t.Error("nil monitor")
	}
}

// The cache never carries the field: only the published copy does.
func TestPublisherAddsFrameAtToTheSnapshotOnly(t *testing.T) {
	old := liveStateSnapshotInterval
	liveStateSnapshotInterval = time.Millisecond
	t.Cleanup(func() { liveStateSnapshotInterval = old })
	store := newMemLiveStateStore()
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	m.mu.Lock()
	m.cache["v"] = &State{VehicleID: "v", PowerState: "ready"}
	m.noteVehicleStateFrameLocked("v")
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	go m.liveStatePublisher(ctx, "v")
	deadline := time.Now().Add(2 * time.Second)
	var got *State
	for got == nil && time.Now().Before(deadline) {
		got, _ = store.GetState(context.Background(), "v")
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if got == nil || got.VehicleStateFrameAt == nil {
		t.Fatalf("published snapshot lacks the frame time: %+v", got)
	}
	if st, _ := m.Latest("v"); st.VehicleStateFrameAt != nil {
		t.Error("the frame time leaked into the cache")
	}
}
