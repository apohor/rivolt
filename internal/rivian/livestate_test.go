package rivian

import (
	"context"
	"testing"
	"time"
)

// TestSnapshotRoundTrip ensures the JSON wire format preserves every
// field that the recorder needs to resume an in-flight drive/charge.
func TestSnapshotRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 5, 7, 19, 0, 0, 0, time.UTC)
	orig := &liveSessions{
		driveCounter:  3,
		chargeCounter: 1,
		drive: &liveDrive{
			id:         "live_01-x_d_1700000000",
			number:     3,
			startedAt:  t0,
			startSoC:   80.5,
			startOdoMi: 12345.6,
			startLat:   30.5538,
			startLon:   -97.7622,
			maxSpeed:   72.1,
			sumSpeed:   1123.4,
			speedN:     17,
			endAt:      t0.Add(7 * time.Minute),
			endSoC:     78.4,
			endOdoMi:   12349.1,
			endLat:     30.5772,
			endLon:     -97.7707,
			path:       [][2]float64{{30.5538, -97.7622}, {30.5772, -97.7707}},
		},
		charge: &liveCharge{
			id:         "live_01-x_c_1690000000",
			number:     1,
			startedAt:  t0.Add(-time.Hour),
			startSoC:   55.0,
			lat:        30.5538,
			lon:        -97.7622,
			maxPower:   7.4,
			endAt:      t0.Add(-time.Minute),
			endSoC:     80.0,
			finalState: "complete",
		},
	}

	snap := orig.snapshot()
	rt := liveSessionsFromSnapshot(snap)

	if rt.driveCounter != orig.driveCounter || rt.chargeCounter != orig.chargeCounter {
		t.Fatalf("counters lost: got %d/%d want %d/%d",
			rt.driveCounter, rt.chargeCounter, orig.driveCounter, orig.chargeCounter)
	}
	if rt.drive == nil || orig.drive == nil {
		t.Fatalf("drive lost across roundtrip")
	}
	d1, d2 := rt.drive, orig.drive
	if d1.id != d2.id || d1.number != d2.number || !d1.startedAt.Equal(d2.startedAt) ||
		d1.startSoC != d2.startSoC || d1.endSoC != d2.endSoC ||
		d1.startLat != d2.startLat || d1.endLat != d2.endLat ||
		d1.maxSpeed != d2.maxSpeed || d1.speedN != d2.speedN || d1.sumSpeed != d2.sumSpeed {
		t.Fatalf("drive scalars lost: got %+v want %+v", d1, d2)
	}
	if len(d1.path) != len(d2.path) {
		t.Fatalf("path length: got %d want %d", len(d1.path), len(d2.path))
	}
	for i := range d1.path {
		if d1.path[i] != d2.path[i] {
			t.Fatalf("path[%d]: got %v want %v", i, d1.path[i], d2.path[i])
		}
	}
	c1, c2 := rt.charge, orig.charge
	if c1 == nil || c2 == nil || c1.id != c2.id || c1.maxPower != c2.maxPower ||
		c1.startSoC != c2.startSoC || c1.endSoC != c2.endSoC || c1.finalState != c2.finalState {
		t.Fatalf("charge lost: got %+v want %+v", c1, c2)
	}
}

// TestMaybeRehydrate_Fresh covers the boot path: pod just started,
// Redis has a snapshot for this vehicle from the previous owner, and
// the next WS frame arrives within liveDriveMaxGap. The accumulator
// must rehydrate, preserving driveCounter so a follow-on lifecycle
// pass does not open a new drive on top of the existing one.
func TestMaybeRehydrate_Fresh(t *testing.T) {
	store := newMemLiveStateStore()
	prev := &liveSessions{
		driveCounter: 4,
		drive: &liveDrive{
			id:        "live_vid-1_d_1700000000",
			number:    4,
			startedAt: time.Date(2026, 5, 7, 19, 0, 0, 0, time.UTC),
			endAt:     time.Date(2026, 5, 7, 19, 7, 0, 0, time.UTC),
			startSoC:  80, endSoC: 78,
		},
	}
	if err := store.Save(context.Background(), "vid-1", prev.snapshot(), 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)

	// Simulate a fresh frame arriving 30 seconds after the snapshot.
	curr := &State{VehicleID: "vid-1", At: time.Date(2026, 5, 7, 19, 7, 30, 0, time.UTC)}
	m.sessMu.Lock()
	got := m.maybeRehydrate("vid-1", curr)
	m.sessMu.Unlock()

	if got.driveCounter != 4 {
		t.Fatalf("driveCounter: got %d want 4 (rehydrate failed)", got.driveCounter)
	}
	if got.drive == nil {
		t.Fatalf("drive: got nil, expected rehydrated drive")
	}
	if got.drive.id != prev.drive.id {
		t.Fatalf("drive.id: got %q want %q", got.drive.id, prev.drive.id)
	}
}

// TestMaybeRehydrate_StaleDriveDropped covers the case where the
// stored snapshot is older than liveDriveMaxGap relative to the
// current frame. The driveCounter still rehydrates (so the next
// open allocates a sensible new number), but the in-flight drive
// itself is dropped so it can't reattach to a finished trip.
func TestMaybeRehydrate_StaleDriveDropped(t *testing.T) {
	store := newMemLiveStateStore()
	prev := &liveSessions{
		driveCounter: 5,
		drive: &liveDrive{
			id:        "live_vid-1_d_1690000000",
			number:    5,
			startedAt: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
			endAt:     time.Date(2026, 5, 7, 12, 10, 0, 0, time.UTC),
		},
	}
	if err := store.Save(context.Background(), "vid-1", prev.snapshot(), 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)

	// Frame arrives 2h after the snapshot — well past liveDriveMaxGap.
	curr := &State{VehicleID: "vid-1", At: time.Date(2026, 5, 7, 14, 10, 0, 0, time.UTC)}
	m.sessMu.Lock()
	got := m.maybeRehydrate("vid-1", curr)
	m.sessMu.Unlock()

	if got.drive != nil {
		t.Fatalf("drive: expected nil (stale), got %+v", got.drive)
	}
	if got.driveCounter != 5 {
		t.Fatalf("driveCounter: counter must survive even when drive is dropped, got %d want 5",
			got.driveCounter)
	}
}

// TestMaybeRehydrate_OncePerVehicle ensures a Redis miss on first
// contact does not keep retrying on every WS frame — we mark the
// vehicle as rehydrated and treat subsequent first-seen-in-process
// events as fresh.
func TestMaybeRehydrate_OncePerVehicle(t *testing.T) {
	store := newMemLiveStateStore() // empty -> Load returns ok=false
	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	curr := &State{VehicleID: "vid-1", At: time.Date(2026, 5, 7, 19, 0, 0, 0, time.UTC)}

	m.sessMu.Lock()
	first := m.maybeRehydrate("vid-1", curr)
	m.sessMu.Unlock()
	if first.drive != nil {
		t.Fatalf("first call: empty store should yield empty sessions")
	}

	// Seed the store AFTER first call. A second call must NOT pick it
	// up — first-contact rehydration is a one-shot per (process,
	// vehicle) until Unsubscribe clears the flag.
	prev := &liveSessions{
		driveCounter: 9,
		drive: &liveDrive{
			id: "should-not-rehydrate", number: 9,
			endAt: curr.At,
		},
	}
	_ = store.Save(context.Background(), "vid-1", prev.snapshot(), 0)

	m.sessMu.Lock()
	second := m.maybeRehydrate("vid-1", curr)
	m.sessMu.Unlock()
	if second.drive != nil {
		t.Fatalf("second call: must not reattempt rehydrate, got %+v", second.drive)
	}
}

// TestMaybeRehydrate_VehicleScoped ensures the snapshot for one
// vehicle never leaks into another vehicle's accumulator.
func TestMaybeRehydrate_VehicleScoped(t *testing.T) {
	store := newMemLiveStateStore()
	other := &liveSessions{
		driveCounter: 7,
		drive: &liveDrive{
			id: "vid-A-drive", number: 7,
			endAt: time.Date(2026, 5, 7, 19, 0, 0, 0, time.UTC),
		},
	}
	_ = store.Save(context.Background(), "vid-A", other.snapshot(), 0)

	m := NewStateMonitor(nil, nil)
	m.SetLiveStateStore(store)
	curr := &State{VehicleID: "vid-B", At: time.Date(2026, 5, 7, 19, 0, 30, 0, time.UTC)}

	m.sessMu.Lock()
	got := m.maybeRehydrate("vid-B", curr)
	m.sessMu.Unlock()
	if got.drive != nil {
		t.Fatalf("vid-B must not pick up vid-A snapshot, got %+v", got.drive)
	}
}

// TestMaybeRehydrate_NoStore is the pre-Redis behaviour: no store
// wired, recorder runs purely from in-memory state. Lazy-init must
// return a fresh empty session and never panic on a nil store.
func TestMaybeRehydrate_NoStore(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	// liveStateStore intentionally not set
	curr := &State{VehicleID: "vid-1", At: time.Date(2026, 5, 7, 19, 0, 0, 0, time.UTC)}
	m.sessMu.Lock()
	got := m.maybeRehydrate("vid-1", curr)
	m.sessMu.Unlock()
	if got == nil || got.drive != nil || got.charge != nil || got.driveCounter != 0 {
		t.Fatalf("nil store: want zero-value session, got %+v", got)
	}
}
