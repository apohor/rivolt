package rivian

import (
	"context"
	"github.com/google/uuid"
	"testing"
	"time"
)

func fakeTouchClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	old := liveTouchNow
	liveTouchNow = func() time.Time { return now }
	t.Cleanup(func() { liveTouchNow = old })
	return &now
}

// A drive in progress is re-upserted at most every liveTouchInterval, not on every
// frame: the open writes once, 100 driving frames in the next few seconds write
// nothing more, and the first frame after the interval writes again. (Each write
// carries the whole encoded polyline, so per-frame writes were O(n^2).)
func TestLiveDriveTouchIsThrottled(t *testing.T) {
	now := fakeTouchClock(t)
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 40, Latitude: 30.0, Longitude: -97.0}, nil, m, ctx)
	if s.drive == nil || s.drive.touch.n != 1 {
		t.Fatalf("after the open: writes %v, want 1", s.drive)
	}
	for i := 1; i <= 100; i++ {
		*now = now.Add(50 * time.Millisecond) // 5 s of frames in total
		_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(time.Duration(i) * 50 * time.Millisecond), Gear: "D",
			OdometerKm: 60000 + float64(i)*0.01, SpeedKph: 40, Latitude: 30.0 + float64(i)*1e-4, Longitude: -97.0}, nil, m, ctx)
	}
	if got := s.drive.touch.n; got != 1 {
		t.Fatalf("100 frames inside the interval wrote %d times in total, want only the open's write", got)
	}
	*now = now.Add(liveTouchInterval)
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(time.Minute), Gear: "D", OdometerKm: 60002, SpeedKph: 40, Latitude: 30.02, Longitude: -97.0}, nil, m, ctx)
	if got := s.drive.touch.n; got != 2 {
		t.Errorf("the first frame after the interval wrote %d times in total, want 2", got)
	}
}

// Closing a drive still writes at once, whatever the throttle says.
func TestLiveDriveCloseWritesImmediately(t *testing.T) {
	fakeTouchClock(t)
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 40}, nil, m, ctx)
	drive := s.drive
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(2 * time.Second), Gear: "P", OdometerKm: 60001, SpeedKph: 0}, nil, m, ctx)
	if s.drive != nil {
		t.Fatal("setup: the drive should be closed")
	}
	if drive.touch.n != 2 {
		t.Errorf("the open and the close wrote %d times, want 2 (the close is not throttled)", drive.touch.n)
	}
}

func TestLiveChargeTouchIsThrottled(t *testing.T) {
	now := fakeTouchClock(t)
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)

	_ = s.handleChargeLifecycle(chargingFrame(t0, 50), nil, m, ctx)
	if s.charge == nil || s.charge.touch.n < 1 {
		t.Fatalf("setup: no charge or no write at the open: %+v", s.charge)
	}
	opened := s.charge.touch.n
	for i := 1; i <= 100; i++ {
		*now = now.Add(50 * time.Millisecond)
		_ = s.handleChargeLifecycle(chargingFrame(t0.Add(time.Duration(i)*50*time.Millisecond), 50+float64(i)*0.01), nil, m, ctx)
	}
	if got := s.charge.touch.n; got != opened {
		t.Fatalf("100 charging frames inside the interval wrote %d more times, want none", got-opened)
	}
	*now = now.Add(liveTouchInterval)
	_ = s.handleChargeLifecycle(chargingFrame(t0.Add(time.Minute), 52), nil, m, ctx)
	if got := s.charge.touch.n; got != opened+1 {
		t.Errorf("first frame after the interval: %d writes, want %d", got, opened+1)
	}
}

// feedDriveSkippingWrites opens a drive and runs frames inside the throttle
// interval, so the stored row is behind the session (dirty).
func feedDriveSkippingWrites(t *testing.T, m *StateMonitor, vid string) *liveDrive {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	m.sessMu.Lock()
	s := m.sessions[vid]
	if s == nil {
		s = &liveSessions{}
		m.sessions[vid] = s
	}
	m.sessMu.Unlock()
	for i := 0; i < 5; i++ {
		_ = s.handleDriveLifecycle(&State{VehicleID: vid, At: t0.Add(time.Duration(i) * time.Second), Gear: "D", OdometerKm: 60000 + float64(i)*0.1, SpeedKph: 40}, nil, m, ctx)
	}
	if s.drive == nil || !s.drive.touch.dirty {
		t.Fatalf("setup: want an open drive with a skipped write, got %+v", s.drive)
	}
	return s.drive
}

// A normal shutdown writes the sessions the throttle left behind (before the
// leases go), so a rollout loses nothing; a second flush has nothing to do.
func TestFlushPendingWritesSkippedSessions(t *testing.T) {
	fakeTouchClock(t)
	m := NewStateMonitor(nil, nil)
	d := feedDriveSkippingWrites(t, m, "v1")
	before := d.touch.n
	r := &MonitorRegistry{monitors: map[uuid.UUID]*monitorEntry{uuid.New(): {monitor: m}}}
	if n := r.FlushPending(context.Background()); n != 1 {
		t.Fatalf("FlushPending wrote %d sessions, want 1", n)
	}
	if d.touch.n != before+1 || d.touch.dirty {
		t.Errorf("after the flush: writes %d (was %d), dirty=%v", d.touch.n, before, d.touch.dirty)
	}
	if n := r.FlushPending(context.Background()); n != 0 {
		t.Errorf("a second flush wrote %d, want 0", n)
	}
}

// A lease hand-off writes that vehicle's skipped session before the peer takes over.
func TestUnsubscribeFlushesTheVehiclesSkippedSession(t *testing.T) {
	fakeTouchClock(t)
	m := NewStateMonitor(nil, nil)
	d := feedDriveSkippingWrites(t, m, "v1")
	other := feedDriveSkippingWrites(t, m, "v2")
	before := d.touch.n
	m.Unsubscribe("v1")
	if d.touch.n != before+1 || d.touch.dirty {
		t.Errorf("v1 after Unsubscribe: writes %d (was %d), dirty=%v", d.touch.n, before, d.touch.dirty)
	}
	if !other.touch.dirty {
		t.Error("another vehicle's session was flushed by v1's hand-off")
	}
}
