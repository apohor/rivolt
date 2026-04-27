package rivian

import (
	"context"
	"testing"
	"time"
)

// chargingFrame builds a State that represents an actively-charging frame.
func chargingFrame(at time.Time, soc float64) *State {
	return &State{
		VehicleID:       "vid-1",
		At:              at,
		BatteryLevelPct: soc,
		ChargerState:    "charging_active",
		ChargerStatus:   "chrgr_sts_connected_charging",
		ChargerPowerKW:  7.4,
	}
}

// TestHandleChargeLifecycle_StaleGapForcesNewSession reproduces the
// `live_*_c_*` row that ran for 32h with EndSoC < StartSoC. A long
// frame gap between two charging frames must close the in-memory
// session and open a fresh one — not silently extend the old one.
func TestHandleChargeLifecycle_StaleGapForcesNewSession(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 4, 20, 0, 0, time.UTC)
	first := chargingFrame(t0, 47.9)
	if got := s.handleChargeLifecycle(first, nil, m, ctx); got != 1 {
		t.Fatalf("first frame: want chargeNum=1, got %d", got)
	}
	firstID := s.charge.id

	stale := chargingFrame(t0.Add(31*time.Hour), 44.0)
	got := s.handleChargeLifecycle(stale, nil, m, ctx)
	if got != 2 {
		t.Fatalf("stale frame: want new chargeNum=2 (forced rotation), got %d", got)
	}
	if s.charge == nil {
		t.Fatalf("stale frame: expected a fresh in-memory charge accumulator")
	}
	if s.charge.id == firstID {
		t.Fatalf("stale frame: expected new session id, still got %q", firstID)
	}
	if s.charge.startSoC != 44.0 {
		t.Fatalf("stale frame: new session must adopt curr SoC as start, got %v", s.charge.startSoC)
	}
}

// TestHandleChargeLifecycle_SoCDropForcesNewSession covers the case
// where Rivian's chargerState/chargerStatus stay sticky across an
// unplug+drive+plugin cycle — we detect via SoC going backwards.
func TestHandleChargeLifecycle_SoCDropForcesNewSession(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 4, 20, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 47.9), nil, m, ctx)
	_ = s.handleChargeLifecycle(chargingFrame(t0.Add(2*time.Minute), 50.0), nil, m, ctx)
	firstID := s.charge.id

	dropped := chargingFrame(t0.Add(5*time.Minute), 44.0)
	got := s.handleChargeLifecycle(dropped, nil, m, ctx)
	if got != 2 {
		t.Fatalf("soc-drop: want new chargeNum=2, got %d", got)
	}
	if s.charge == nil || s.charge.id == firstID {
		t.Fatalf("soc-drop: expected a fresh session, charge=%+v", s.charge)
	}
	if s.charge.startSoC != 44.0 {
		t.Fatalf("soc-drop: new session must adopt curr SoC as start, got %v", s.charge.startSoC)
	}
}

// TestHandleChargeLifecycle_NormalFrameKeepsSession is the negative
// control: small gaps and rising SoC must NOT rotate the session.
func TestHandleChargeLifecycle_NormalFrameKeepsSession(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 4, 20, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 47.9), nil, m, ctx)
	firstID := s.charge.id

	for i := 1; i <= 5; i++ {
		f := chargingFrame(t0.Add(time.Duration(i)*time.Minute), 47.9+float64(i))
		_ = s.handleChargeLifecycle(f, nil, m, ctx)
	}
	if s.charge == nil || s.charge.id != firstID {
		t.Fatalf("normal frames must keep the same session id; want %q, got %+v", firstID, s.charge)
	}
}

// TestHandleDriveLifecycle_StaleGapForcesNewSession is the drive
// analogue: a long frame gap with the gear still in D means the WS
// almost certainly straddled two real drives.
func TestHandleDriveLifecycle_StaleGapForcesNewSession(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 8, 0, 0, 0, time.UTC)
	first := &State{VehicleID: "vid-1", At: t0, Gear: "D", BatteryLevelPct: 80, OdometerKm: 1000}
	if got := s.handleDriveLifecycle(first, nil, m, ctx); got != 1 {
		t.Fatalf("first drive frame: want driveNum=1, got %d", got)
	}
	firstID := s.drive.id

	stale := &State{VehicleID: "vid-1", At: t0.Add(2 * time.Hour), Gear: "D", BatteryLevelPct: 60, OdometerKm: 1100}
	got := s.handleDriveLifecycle(stale, nil, m, ctx)
	if got != 2 {
		t.Fatalf("stale drive frame: want new driveNum=2, got %d", got)
	}
	if s.drive == nil || s.drive.id == firstID {
		t.Fatalf("stale drive frame: expected fresh session, got %+v", s.drive)
	}
}

// TestApplyMutualExclusion_DrivingClosesCharge verifies the physical
// invariant: a car reporting a driving gear cannot have an open
// charge accumulator. This is the primary fix for the 4/26 32h-row
// bug — a drive between two plug-ins must not be absorbed into one
// charge session.
func TestApplyMutualExclusion_DrivingClosesCharge(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 4, 20, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 47.9), nil, m, ctx)
	if s.charge == nil {
		t.Fatalf("setup: charge should be open")
	}

	driving := &State{
		VehicleID: "vid-1", At: t0.Add(10 * time.Minute),
		Gear: "D", BatteryLevelPct: 60, OdometerKm: 1000,
	}
	s.applyMutualExclusion(driving, m, ctx)
	if s.charge != nil {
		t.Fatalf("driving frame must close charge accumulator, got %+v", s.charge)
	}
}

// TestApplyMutualExclusion_ChargingClosesDrive is the reverse: a
// frame that shows the car plugged in and charging must close any
// open drive accumulator (e.g. WS dropped mid-drive, then reconnected
// while the car was already plugged in at home).
func TestApplyMutualExclusion_ChargingClosesDrive(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	_ = s.handleDriveLifecycle(&State{VehicleID: "vid-1", At: t0, Gear: "D", BatteryLevelPct: 80, OdometerKm: 1000}, nil, m, ctx)
	if s.drive == nil {
		t.Fatalf("setup: drive should be open")
	}

	s.applyMutualExclusion(chargingFrame(t0.Add(time.Minute), 60), m, ctx)
	if s.drive != nil {
		t.Fatalf("charging frame must close drive accumulator, got %+v", s.drive)
	}
}

// TestApplyMutualExclusion_NeutralFrameTouchesNothing is the negative
// control: a parked, unplugged frame must not disturb either side.
func TestApplyMutualExclusion_NeutralFrameTouchesNothing(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 47.9), nil, m, ctx)
	chargeID := s.charge.id

	parked := &State{VehicleID: "vid-1", At: t0.Add(time.Minute), Gear: "P"}
	s.applyMutualExclusion(parked, m, ctx)
	if s.charge == nil || s.charge.id != chargeID {
		t.Fatalf("parked + unplugged frame must NOT touch charge accumulator")
	}
}
