package rivian

import (
	"context"
	"testing"
	"time"
)

// An R2 after a restart: vehicleState gives no power state, and the cloud
// replays the latest power.state frame (the last change, hours old). It fills
// the blank, never replaces a value, and a later real change still applies.
func TestR2PowerStateSeededFromAReplay(t *testing.T) {
	ctx := context.Background()
	push := func(r *replayRun, power string) {
		r.m.applyVehicleStatePush(ctx, replayVehicle, &State{VehicleID: replayVehicle, At: time.Now(), Gear: "P",
			PowerState: power, BatteryLevelPct: 60, OdometerKm: 30000})
	}
	frame := func(r *replayRun, enum uint64, at time.Time) {
		r.m.onDriveDynamicsFrame(ctx, replayVehicle, DriveDynamicsFrame{RVM: rvmPowerState,
			Payload: pbVarints(1, enum), Value: enum, ValueOK: true, TimestampMs: at.UnixMilli()})
	}
	power := func(r *replayRun) string { return r.cached().PowerState }

	r := newReplayRun("R2")
	push(r, "")
	if power(r) != "" {
		t.Fatalf("setup: power %q", power(r))
	}
	frame(r, 1, time.Now().Add(-7*time.Hour)) // replay of a sleep from last night
	if power(r) != "sleep" {
		t.Fatalf("a replayed sleep left the state %q, want sleep", power(r))
	}
	if got := r.cached().PowerStateAt; time.Since(got) < 6*time.Hour {
		t.Errorf("PowerStateAt = %v, want the frame's own time", got)
	}
	push(r, "") // the next vehicleState push without the field keeps it
	if power(r) != "sleep" {
		t.Errorf("a push blanked it: %q", power(r))
	}
	// A replayed "ready" must not replace what the cache has.
	frame(r, 3, time.Now().Add(-6*time.Hour))
	if power(r) != "sleep" {
		t.Errorf("a replay replaced the value: %q", power(r))
	}
	// And a real change still applies.
	frame(r, 3, time.Now())
	if power(r) != "ready" {
		t.Errorf("a fresh frame: %q, want ready", power(r))
	}

	// An unmapped enum seeds nothing.
	r2 := newReplayRun("R2")
	push(r2, "")
	frame(r2, 0, time.Now().Add(-time.Hour))
	if power(r2) != "" {
		t.Errorf("unmapped enum seeded %q", power(r2))
	}
}
