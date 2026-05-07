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

// chargingFrameKW is like chargingFrame but lets the caller override
// the per-frame charger_power_kw — used to exercise the maxLivePowerKW
// outlier cap (see v0.10.x phantom-charges incident: a single bad
// Parallax frame reported ~90 MW and ratcheted the session peak).
func chargingFrameKW(at time.Time, soc, kw float64) *State {
	f := chargingFrame(at, soc)
	f.ChargerPowerKW = kw
	return f
}

// TestHandleChargeLifecycle_OutlierPowerFrameIgnored ensures a single
// physically-impossible charger_power_kw frame does NOT update the
// running max. Real Rivian packs accept ~220 kW peak; anything above
// maxLivePowerKW is a corrupt wire frame and must be discarded, not
// clamped (clamping would silently set the session peak to the cap).
func TestHandleChargeLifecycle_OutlierPowerFrameIgnored(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 4, 28, 17, 7, 0, 0, time.UTC)
	// Open with a normal 7.4 kW frame.
	_ = s.handleChargeLifecycle(chargingFrame(t0, 70), nil, m, ctx)
	if s.charge == nil || s.charge.maxPower != 7.4 {
		t.Fatalf("setup: want maxPower=7.4, got %+v", s.charge)
	}

	// One bogus 90,897.9 kW frame — exactly what we saw on the
	// 4/28 phantom DC-ultra row in production.
	_ = s.handleChargeLifecycle(chargingFrameKW(t0.Add(time.Second), 70.1, 90897.9), nil, m, ctx)
	if s.charge.maxPower > maxLivePowerKW {
		t.Fatalf("outlier frame leaked into maxPower: got %v, want <= %v", s.charge.maxPower, maxLivePowerKW)
	}
	if s.charge.maxPower != 7.4 {
		t.Fatalf("outlier frame should be discarded, not clamped: got %v, want 7.4", s.charge.maxPower)
	}

	// A plausible 200 kW frame must still update the peak.
	_ = s.handleChargeLifecycle(chargingFrameKW(t0.Add(2*time.Second), 70.2, 200), nil, m, ctx)
	if s.charge.maxPower != 200 {
		t.Fatalf("plausible 200 kW frame must update maxPower: got %v", s.charge.maxPower)
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

// TestHandleChargeLifecycle_CloseUsesLatestFrameSoC reproduces the
// 5/1 8861cda4 row: WS goes silent mid-charge, reconnects only after
// the car has finished charging, and the very first post-gap frame
// is already charging_complete + plug-disconnected. Without pulling
// the latest frame's SoC into the row before persisting, the close
// branch writes the pre-gap endSoC (76.6%) even though the car
// charged all the way to 95%.
//
// We can't observe the persisted row directly without a real store,
// so we hook the mutation by intercepting it via a sentinel inserted
// before the close: drive an "ongoing" frame at 95% (still charging
// + plugged) so handleChargeLifecycle stays on the ongoing branch
// and we can read s.charge.endSoC, then do a separate close pass.
// The close branch's job is to forward those same mutations when
// the *first* post-gap frame is already terminal — what this test
// pins down.
func TestHandleChargeLifecycle_CloseUsesLatestFrameSoC(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 5, 1, 1, 53, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 63.7), nil, m, ctx)
	_ = s.handleChargeLifecycle(chargingFrame(t0.Add(2*time.Hour+26*time.Minute), 76.6), nil, m, ctx)
	if s.charge == nil || s.charge.endSoC < 76.0 || s.charge.endSoC > 77.0 {
		t.Fatalf("setup: charge endSoC should be ~76.6, got %+v", s.charge)
	}
	preGapEndSoC := s.charge.endSoC
	preGapEndAt := s.charge.endAt

	// 3.5h WS gap, then reconnect: first post-gap frame says the car
	// is already done charging at 95%, plug disconnected. This frame
	// must hit the close branch, not the ongoing branch.
	postGap := &State{
		VehicleID:       "vid-1",
		At:              t0.Add(6 * time.Hour),
		BatteryLevelPct: 95,
		ChargerState:    "charging_complete",
		ChargerStatus:   "chrgr_sts_disconnected",
	}
	// We can't read s.charge after the close (the branch nils it
	// out). But the close branch is required to mutate s.charge in
	// place BEFORE calling upsert. To verify, we tee a copy by
	// calling handleChargeLifecycle in a goroutine-free way: stash
	// a pointer alias before invocation, observe via the alias.
	chargeRef := s.charge
	_ = s.handleChargeLifecycle(postGap, nil, m, ctx)
	if s.charge != nil {
		t.Fatalf("close branch must clear in-memory charge accumulator")
	}
	if chargeRef.endSoC == preGapEndSoC {
		t.Fatalf("close branch must advance endSoC from pre-gap value (%v); still got %v", preGapEndSoC, chargeRef.endSoC)
	}
	if chargeRef.endSoC < 94 {
		t.Fatalf("close branch must adopt latest frame's 95%% SoC; got %v", chargeRef.endSoC)
	}
	if !chargeRef.endAt.After(preGapEndAt) {
		t.Fatalf("close branch must advance endAt past pre-gap (%v); got %v", preGapEndAt, chargeRef.endAt)
	}
}

// TestHandleDriveLifecycle_StaleClockDoesNotFragment reproduces a
// past incident: a single 90-min drive produced 60+ drive rows
// because Rivian's WS deltas alternate between including a
// GNSSLocation block (carrying a stale 38h-old GPS fix timestamp,
// which earlier code assigned to State.At) and omitting it (in
// which case parseTimeOrNow returned time.Now). Each fresh-At frame
// after a stale-At frame saw curr.At - s.drive.endAt > 30min and
// triggered the "closing stale live drive" path, fragmenting the
// real drive into 3-min stubs. With State.At now sourced from wall
// clock and a monotonic-forward endAt guard, alternating stale/fresh
// inputs must produce ONE drive row.
func TestHandleDriveLifecycle_StaleClockDoesNotFragment(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 5, 3, 18, 1, 0, 0, time.UTC)
	open := &State{VehicleID: "vid-1", At: t0, Gear: "D", BatteryLevelPct: 80, OdometerKm: 1000}
	if got := s.handleDriveLifecycle(open, nil, m, ctx); got != 1 {
		t.Fatalf("open: want driveNum=1, got %d", got)
	}
	firstID := s.drive.id

	// Simulate 30 frames of an actual drive, alternating fresh wall
	// clock (each 3 min later) with frames carrying a GPS fix from
	// 38h ago (the past root cause).
	staleGPS := t0.Add(-38 * time.Hour)
	for i := 1; i <= 30; i++ {
		var at time.Time
		if i%2 == 0 {
			at = staleGPS // would have regressed endAt under earlier code
		} else {
			at = t0.Add(time.Duration(i) * 3 * time.Minute)
		}
		f := &State{VehicleID: "vid-1", At: at, Gear: "D", BatteryLevelPct: 80 - float64(i)*0.5, OdometerKm: 1000 + float64(i)*2, SpeedKph: 100}
		_ = s.handleDriveLifecycle(f, nil, m, ctx)
		if s.drive == nil || s.drive.id != firstID {
			t.Fatalf("frame %d (at=%v): drive must remain a single session id, got %+v", i, at, s.drive)
		}
		if s.drive.endAt.Before(t0) {
			t.Fatalf("frame %d: endAt must never regress before drive open; got %v", i, s.drive.endAt)
		}
	}
}

// TestHandleDriveLifecycle_EndAtMonotonic: a single regressed-clock
// frame (e.g. an out-of-order push) must not pull endAt backwards.
func TestHandleDriveLifecycle_EndAtMonotonic(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 5, 3, 18, 1, 0, 0, time.UTC)
	_ = s.handleDriveLifecycle(&State{VehicleID: "vid-1", At: t0, Gear: "D", BatteryLevelPct: 80}, nil, m, ctx)
	_ = s.handleDriveLifecycle(&State{VehicleID: "vid-1", At: t0.Add(10 * time.Minute), Gear: "D", BatteryLevelPct: 78}, nil, m, ctx)
	advanced := s.drive.endAt
	// Out-of-order frame at t0+5m must not regress endAt.
	_ = s.handleDriveLifecycle(&State{VehicleID: "vid-1", At: t0.Add(5 * time.Minute), Gear: "D", BatteryLevelPct: 79}, nil, m, ctx)
	if !s.drive.endAt.Equal(advanced) {
		t.Fatalf("endAt must be monotonic; want %v got %v", advanced, s.drive.endAt)
	}
}

// TestHandleChargeLifecycle_EndAtMonotonic: same monotonic-forward
// invariant for charges. A regressed-clock close frame must not
// produce a row with ended_at < started_at (the 2026-05-03 incident).
func TestHandleChargeLifecycle_EndAtMonotonic(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 5, 3, 4, 0, 0, 0, time.UTC)
	_ = s.handleChargeLifecycle(chargingFrame(t0, 50), nil, m, ctx)
	_ = s.handleChargeLifecycle(chargingFrame(t0.Add(time.Hour), 60), nil, m, ctx)
	advanced := s.charge.endAt
	chargeRef := s.charge

	// A close frame with a regressed clock (38h ago) must not leak
	// into the persisted endAt.
	close := &State{
		VehicleID:       "vid-1",
		At:              t0.Add(-38 * time.Hour),
		BatteryLevelPct: 65,
		ChargerState:    "charging_complete",
		ChargerStatus:   "chrgr_sts_disconnected",
	}
	_ = s.handleChargeLifecycle(close, nil, m, ctx)
	if chargeRef.endAt.Before(advanced) {
		t.Fatalf("close branch leaked regressed clock into endAt: was %v, became %v", advanced, chargeRef.endAt)
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
