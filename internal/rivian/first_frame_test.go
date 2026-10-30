package rivian

import (
	"testing"
	"time"
)

// FirstFrameAt is this ownership's first vehicleState frame: later frames do not move it, and a release
// (forgetVehicle) clears it so the next ownership gets its own. (The lease coordinator's acquire-to-frame
// metric read the newest frame's time before, which made a vehicle observed at the next 30 s tick look
// like it had waited 20 to 30 s.)
func TestFirstFrameAtIsThisOwnershipsFirstFrame(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	now := time.Date(2026, 10, 30, 19, 33, 32, 0, time.UTC)
	m.now = func() time.Time { return now }
	if !m.FirstFrameAt("veh").IsZero() {
		t.Fatal("a first frame before any frame")
	}
	at := func(sec int) {
		now = time.Date(2026, 10, 30, 19, 33, sec, 0, time.UTC)
		m.mu.Lock()
		m.noteVehicleStateFrameLocked("veh")
		m.mu.Unlock()
	}
	at(33)
	first := m.FirstFrameAt("veh")
	if !first.Equal(time.Date(2026, 10, 30, 19, 33, 33, 0, time.UTC)) {
		t.Fatalf("first frame = %v", first)
	}
	at(50)
	at(59)
	if got := m.FirstFrameAt("veh"); !got.Equal(first) {
		t.Fatalf("later frames moved the first frame to %v, want %v", got, first)
	}
	m.forgetVehicle("veh") // the lease is released
	if !m.FirstFrameAt("veh").IsZero() {
		t.Fatal("the first frame survived the release")
	}
	at(10)
	if got := m.FirstFrameAt("veh"); !got.Equal(time.Date(2026, 10, 30, 19, 33, 10, 0, time.UTC)) {
		t.Fatalf("the next ownership's first frame = %v", got)
	}
}
