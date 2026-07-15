package rivian

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
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

// idleStickyFrame mimics what Rivian sends after a user unplugs and
// drives away without the gateway noticing: chargerState parks at
// charging_ready, the plug status stays "connected" (sticky), and
// chargerPowerKW drifts at the parasitic floor (~0.1 kW). Used to
// reproduce the 40-hour ghost session class (charges row 6ce9).
func idleStickyFrame(at time.Time, soc float64) *State {
	return &State{
		VehicleID:       "vid-1",
		At:              at,
		BatteryLevelPct: soc,
		ChargerState:    "charging_ready",
		ChargerStatus:   "chrgr_sts_connected",
		ChargerPowerKW:  0.1,
	}
}

// TestHandleChargeLifecycle_IdleStickyReplayForcesClose pins the fix
// for the 40-hour ghost session (charges row 6ce9). The gateway
// replays charging_ready + connected + sub-floor power for hours
// while the vehicle is physically gone. The old guard measured its
// gap against endAt — which advanced every tick — so it never fired,
// and accumulateEnergy summed the parasitic draw into ~54 kWh of
// phantom energy. The fix: gap is measured against lastMeaningfulAt
// (only bumped on SoC-up / above-floor power / state transition),
// and the integrator skips sub-floor frames entirely.
func TestHandleChargeLifecycle_IdleStickyReplayForcesClose(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()

	t0 := time.Date(2026, 5, 24, 4, 5, 43, 0, time.UTC)
	// Real charging starts: SoC climbing, full L2 power.
	_ = s.handleChargeLifecycle(chargingFrame(t0, 50.0), nil, m, ctx)
	_ = s.handleChargeLifecycle(chargingFrame(t0.Add(10*time.Minute), 53.0), nil, m, ctx)
	firstID := s.charge.id
	energyAfterReal := s.charge.energyIntKWh

	// Replay sticky idle frames for an hour: same chargerState, sub-floor
	// power, flat SoC. Each frame is < liveChargeMaxGap apart so the
	// old endAt-based gap check never tripped.
	for i := 1; i <= 12; i++ {
		f := idleStickyFrame(t0.Add(10*time.Minute+time.Duration(i)*5*time.Minute), 53.0)
		_ = s.handleChargeLifecycle(f, nil, m, ctx)
	}

	// One more idle frame past the 30-min meaningful-gap threshold.
	// lastMeaningfulAt is pinned at t0+10m (last SoC bump); 35 min
	// later the guard must trip.
	postGuard := idleStickyFrame(t0.Add(10*time.Minute+35*time.Minute), 53.0)
	got := s.handleChargeLifecycle(postGuard, nil, m, ctx)
	if got != 2 {
		t.Fatalf("idle-sticky: want new chargeNum=2 (forced rotation), got %d", got)
	}
	if s.charge == nil || s.charge.id == firstID {
		t.Fatalf("idle-sticky: expected fresh session, got %+v (firstID=%q)", s.charge, firstID)
	}
	// Energy must not have grown during the idle stretch — the
	// accumulator should have short-circuited sub-floor frames.
	// The new session's integral starts from zero anyway, so the
	// invariant we check is on the new charge.
	if s.charge.energyIntKWh > 0.01 {
		t.Fatalf("idle-sticky: new session must start with zero energy, got %v", s.charge.energyIntKWh)
	}
	_ = energyAfterReal // referenced for documentation; new session's integral is the assertion
}

// TestAccumulateEnergy_IdleFramesDontIntegrate is a unit-level pin on
// the accumulator's sub-floor short-circuit. A long stretch of
// parasitic-power frames must add zero kWh, even if the surrounding
// frames had real power (the anchor must be cleared so the trapezoid
// across the gap doesn't carry forward).
func TestAccumulateEnergy_IdleFramesDontIntegrate(t *testing.T) {
	c := &liveCharge{}
	t0 := time.Date(2026, 5, 24, 4, 0, 0, 0, time.UTC)

	// 10 minutes of real charging at 7 kW, sampled every minute so
	// the maxIntegrationGap clamp (2 min) doesn't trim each step.
	// Expected: 7 kW * 10/60 h ≈ 1.17 kWh.
	accumulateEnergy(c, t0, 7.0)
	for i := 1; i <= 10; i++ {
		accumulateEnergy(c, t0.Add(time.Duration(i)*time.Minute), 7.0)
	}
	realEnergy := c.energyIntKWh
	if realEnergy < 1.0 || realEnergy > 1.3 {
		t.Fatalf("baseline: 10 min @ 7 kW should give ~1.17 kWh, got %v", realEnergy)
	}

	// 2 hours of idle at 0.1 kW (parasitic). The integral must NOT
	// grow — sub-floor frames clear the anchor and skip integration.
	for i := 1; i <= 24; i++ {
		accumulateEnergy(c, t0.Add(10*time.Minute+time.Duration(i)*5*time.Minute), 0.1)
	}
	if c.energyIntKWh != realEnergy {
		t.Fatalf("idle frames must not integrate: was %v, became %v (delta %v)",
			realEnergy, c.energyIntKWh, c.energyIntKWh-realEnergy)
	}

	// Resume real charging: a 7 kW frame after the idle stretch must
	// NOT integrate across the gap (which would add ~3.5 kW * 2h = 7
	// kWh of phantom energy). The anchor was cleared on each idle
	// frame, so this frame starts a fresh anchor and adds nothing
	// until the next active frame arrives.
	accumulateEnergy(c, t0.Add(2*time.Hour+10*time.Minute), 7.0)
	if c.energyIntKWh != realEnergy {
		t.Fatalf("first post-idle active frame must not integrate across the idle gap: was %v, became %v",
			realEnergy, c.energyIntKWh)
	}

	// Next active frame 1 minute later: ~0.12 kWh added (real charging).
	accumulateEnergy(c, t0.Add(2*time.Hour+11*time.Minute), 7.0)
	if c.energyIntKWh-realEnergy < 0.10 || c.energyIntKWh-realEnergy > 0.13 {
		t.Fatalf("post-idle 1 min @ 7 kW should add ~0.117 kWh, total now %v (delta %v)",
			c.energyIntKWh, c.energyIntKWh-realEnergy)
	}
}

// TestRestoredChargeSurvivesIdleFrameAfterRestart reproduces the
// split-on-deploy incident (preview 8d94 -> 7180): a multi-hour charge
// that's restored after a pod restart must NOT be abandoned on the
// first idle frame. The stale-session guard anchors on lastMeaningfulAt;
// if a restore left that zero it fell back to startedAt (hours ago),
// blew past liveChargeMaxGap, and abandoned the live session. Restore
// now anchors lastMeaningfulAt at the snapshot's EndAt.
func TestRestoredChargeSurvivesIdleFrameAfterRestart(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	ctx := context.Background()

	t0 := time.Date(2026, 5, 28, 18, 42, 15, 0, time.UTC)
	endAt := t0.Add(2*time.Hour + 20*time.Minute) // last frame before restart
	snap := LiveStateSnapshot{
		ChargeCounter: 1,
		Charge: &LiveChargeSnapshot{
			ID: "live_vid-1_c_resume", Number: 1, StartedAt: t0,
			StartSoC: 51, EndAt: endAt, EndSoC: 61,
			FinalState: "charging_active", MaxPower: 7.4,
			LastMeaningfulAt: endAt,
		},
	}
	s := liveSessionsFromSnapshot(snap)
	if s.charge == nil || s.charge.lastMeaningfulAt.IsZero() {
		t.Fatalf("restore must carry a non-zero lastMeaningfulAt, got %+v", s.charge)
	}

	// First frame after restart, ~30s later, idle-sticky (power below the
	// floor, SoC flat). Gap from EndAt is tiny, so the guard must NOT fire.
	post := idleStickyFrame(endAt.Add(30*time.Second), 61)
	got := s.handleChargeLifecycle(post, nil, m, ctx)
	if got != 1 {
		t.Fatalf("restored charge must continue as session 1, got %d (split)", got)
	}
	if s.charge == nil || s.charge.id != "live_vid-1_c_resume" {
		t.Fatalf("restored charge was abandoned/replaced: %+v", s.charge)
	}
	if s.charge.finalState == "abandoned" {
		t.Fatalf("restored charge must not be abandoned on first idle frame")
	}
}

// TestLiveSessionsFromSnapshot_FallsBackToEndAt covers older snapshots
// written before the LastMeaningfulAt field existed: a zero value must
// fall back to EndAt, not leak through as the guard's zero-time anchor.
func TestLiveSessionsFromSnapshot_FallsBackToEndAt(t *testing.T) {
	endAt := time.Date(2026, 5, 28, 21, 0, 0, 0, time.UTC)
	snap := LiveStateSnapshot{
		Charge: &LiveChargeSnapshot{
			ID: "c1", StartedAt: endAt.Add(-2 * time.Hour), EndAt: endAt,
			// LastMeaningfulAt deliberately zero (legacy snapshot).
		},
	}
	s := liveSessionsFromSnapshot(snap)
	if s.charge == nil || !s.charge.lastMeaningfulAt.Equal(endAt) {
		t.Fatalf("legacy snapshot must fall back to EndAt, got %v", s.charge.lastMeaningfulAt)
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

// TestDriveCloseHook_FiresOnDtoP verifies the DriveCloseHook is
// invoked exactly once per D→P transition, with the persisted drive
// row's shape (id, vehicle, start/end times). Open and ongoing
// frames must NOT fire the hook.
func TestDriveCloseHook_FiresOnDtoP(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	type call struct {
		drv  drives.Drive
		seen bool
	}
	var got call
	done := make(chan struct{})
	m.SetDriveCloseHook(func(_ context.Context, d drives.Drive) {
		got.drv = d
		got.seen = true
		close(done)
	})

	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	// Open: gear D → drive opens.
	_ = s.handleDriveLifecycle(&State{
		VehicleID: "vid-1", At: t0, Gear: "D",
		BatteryLevelPct: 80, OdometerKm: 1000,
		Latitude: 30.55, Longitude: -97.76,
	}, nil, m, ctx)
	if got.seen {
		t.Fatalf("hook fired on drive OPEN; should only fire on close")
	}

	// Ongoing: another D frame, still driving.
	_ = s.handleDriveLifecycle(&State{
		VehicleID: "vid-1", At: t0.Add(5 * time.Minute), Gear: "D",
		BatteryLevelPct: 79, OdometerKm: 1010, SpeedKph: 100,
		Latitude: 30.6, Longitude: -97.8,
	}, nil, m, ctx)
	if got.seen {
		t.Fatalf("hook fired on ongoing drive frame; should only fire on close")
	}

	// Close: gear P.
	openID := s.drive.id
	_ = s.handleDriveLifecycle(&State{
		VehicleID: "vid-1", At: t0.Add(10 * time.Minute), Gear: "P",
		BatteryLevelPct: 78, OdometerKm: 1020,
		Latitude: 30.65, Longitude: -97.85,
	}, nil, m, ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("hook did not fire on D→P close")
	}

	if got.drv.ID != openID {
		t.Fatalf("hook drive id: got %q want %q (open's id)", got.drv.ID, openID)
	}
	if got.drv.VehicleID != "vid-1" {
		t.Fatalf("hook drive vehicle: got %q", got.drv.VehicleID)
	}
	if !got.drv.StartedAt.Equal(t0) {
		t.Fatalf("hook startedAt: got %v want %v", got.drv.StartedAt, t0)
	}
}

// TestDriveCloseHook_NotSetIsNoOp ensures the recorder behaves
// identically when no hook is wired (the chart-default path).
func TestDriveCloseHook_NotSetIsNoOp(t *testing.T) {
	m := NewStateMonitor(nil, nil) // no SetDriveCloseHook
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{
		VehicleID: "vid-1", At: t0, Gear: "D", BatteryLevelPct: 80,
	}, nil, m, ctx)
	got := s.handleDriveLifecycle(&State{
		VehicleID: "vid-1", At: t0.Add(time.Minute), Gear: "P",
		BatteryLevelPct: 79,
	}, nil, m, ctx)

	if got != 1 {
		t.Fatalf("close should still return drive number 1; got %d", got)
	}
	if s.drive != nil {
		t.Fatalf("drive should be cleared after close")
	}
}

// TestHandleDriveLifecycle_PhantomPBlipDoesNotClose reproduces the
// 2026-07-14 22:27 split: vehicleState emitted P for two frames at 5 mph
// mid-drive, then D again 3s later — one trip fragmented into two rows.
// A P frame with the car still moving must not close the drive.
func TestHandleDriveLifecycle_PhantomPBlipDoesNotClose(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 22, 27, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 40}, nil, m, ctx)
	firstID := s.drive.id

	// Phantom P at 5 mph (8 kph) — must keep the drive open and keep
	// stamping the frame with the open drive number.
	if got := s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(46 * time.Second), Gear: "P", OdometerKm: 60000.5, SpeedKph: 8}, nil, m, ctx); got != 1 {
		t.Fatalf("phantom P: want driveNum=1 (still open), got %d", got)
	}
	if s.drive == nil {
		t.Fatal("phantom P at 5 mph must not close the drive")
	}
	// D returns 3s later — same session, blip cancelled.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(49 * time.Second), Gear: "D", OdometerKm: 60000.5, SpeedKph: 8}, nil, m, ctx)
	if s.drive == nil || s.drive.id != firstID {
		t.Fatalf("blip must not fragment: want id %s, got %+v", firstID, s.drive)
	}
	if !s.drive.pendingCloseAt.IsZero() {
		t.Fatal("returning to D must clear pendingCloseAt")
	}
}

// TestHandleDriveLifecycle_StoppedParkClosesImmediately: the normal stop
// (P with speed ~0) must close on the first frame, no debounce latency.
func TestHandleDriveLifecycle_StoppedParkClosesImmediately(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 22, 0, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 40}, nil, m, ctx)
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(time.Minute), Gear: "P", OdometerKm: 60001, SpeedKph: 0}, nil, m, ctx)
	if s.drive != nil {
		t.Fatal("P at 0 mph must close immediately")
	}
}

// TestHandleDriveLifecycle_StaleSpeedPersistCloses reproduces the
// 2026-07-14 23:17 blackout close: the parking frame carried a stale
// 18 mph speed reading. P persisting past driveCloseDebounce must close
// the drive even though speed never reads zero.
func TestHandleDriveLifecycle_StaleSpeedPersistCloses(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 23, 12, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 30}, nil, m, ctx)
	// P frames with stale 18 mph (29 kph): first two within debounce.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(60 * time.Second), Gear: "P", OdometerKm: 60002, SpeedKph: 29}, nil, m, ctx)
	if s.drive == nil {
		t.Fatal("first stale-speed P frame must not close yet")
	}
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(65 * time.Second), Gear: "P", OdometerKm: 60002, SpeedKph: 29}, nil, m, ctx)
	if s.drive == nil {
		t.Fatal("5s of P is inside the debounce window")
	}
	// Past the debounce: close commits, and the parking frame's higher
	// odometer was folded in by the gap-fix.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(71 * time.Second), Gear: "P", OdometerKm: 60002, SpeedKph: 29}, nil, m, ctx)
	if s.drive != nil {
		t.Fatal("P persisting past driveCloseDebounce must close")
	}
	if s.lastClosed == nil || s.lastClosed.endOdoMi < 60001.9*kmToMi {
		t.Fatalf("gap-fix odometer fold missing on debounced close: %+v", s.lastClosed)
	}
}

// TestHandleDriveLifecycle_MergeOnReopen reproduces the 23:26 parking-lot
// shuffle: close, then D again 10s later with the odometer unmoved —
// the previous drive row must resume instead of fragmenting.
func TestHandleDriveLifecycle_MergeOnReopen(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 23, 26, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 20}, nil, m, ctx)
	firstID := s.drive.id
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(30 * time.Second), Gear: "P", OdometerKm: 60000.1, SpeedKph: 0}, nil, m, ctx)
	if s.drive != nil {
		t.Fatal("setup: drive should be closed")
	}
	// Reopen 10s later, odometer unmoved → resume the same row.
	if got := s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(40 * time.Second), Gear: "D", OdometerKm: 60000.1, SpeedKph: 10}, nil, m, ctx); got != 1 {
		t.Fatalf("reopen: want resumed driveNum=1, got %d", got)
	}
	if s.drive == nil || s.drive.id != firstID {
		t.Fatalf("reopen within window must resume id %s, got %+v", firstID, s.drive)
	}
	// A reopen far beyond the window must be a new drive.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(50 * time.Second), Gear: "P", OdometerKm: 60000.2, SpeedKph: 0}, nil, m, ctx)
	if got := s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(10 * time.Minute), Gear: "D", OdometerKm: 60000.2, SpeedKph: 10}, nil, m, ctx); got != 2 {
		t.Fatalf("reopen past window: want new driveNum=2, got %d", got)
	}
	if s.drive.id == firstID {
		t.Fatal("reopen past window must not resume the old id")
	}
}

// TestHandleDriveLifecycle_CloseSplicesParkingFix: the close-time
// endpoint splice must extend the path to the parking frame's location
// (after a blackout the numbers were fixed but the map ended mid-road).
func TestHandleDriveLifecycle_CloseSplicesParkingFix(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 23, 11, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 20, Latitude: 30.55, Longitude: -97.76}, nil, m, ctx)
	// Blackout … then the parking frame arrives at a new location.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(6 * time.Minute), Gear: "P", OdometerKm: 60003, SpeedKph: 0, Latitude: 30.52, Longitude: -97.77}, nil, m, ctx)
	if s.drive != nil {
		t.Fatal("setup: drive should be closed")
	}
	path := s.lastClosed.path
	if len(path) < 2 {
		t.Fatalf("parking fix must be appended to the path, got %v", path)
	}
	end := path[len(path)-1]
	if end[0] != 30.52 || end[1] != -97.77 {
		t.Fatalf("path must end at the parking spot, got %v", end)
	}
}

// TestHandleDriveLifecycle_StaleFixReplayRejected: with Parallax gnss as
// the GPS backbone, a vehicleState frame replaying an older fix (by
// LocationFixAt) must not drag the path backwards.
func TestHandleDriveLifecycle_StaleFixReplayRejected(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	s := &liveSessions{}
	ctx := context.Background()
	t0 := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)

	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0, Gear: "D", OdometerKm: 60000, SpeedKph: 20,
		Latitude: 30.50, Longitude: -97.70, LocationFixAt: t0}, nil, m, ctx)
	// Fresh (Parallax) fix.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(30 * time.Second), Gear: "D", OdometerKm: 60000.5, SpeedKph: 20,
		Latitude: 30.51, Longitude: -97.71, LocationFixAt: t0.Add(30 * time.Second)}, nil, m, ctx)
	// Stale vehicleState replay: older LocationFixAt, old coords.
	_ = s.handleDriveLifecycle(&State{VehicleID: "v", At: t0.Add(33 * time.Second), Gear: "D", OdometerKm: 60000.5, SpeedKph: 20,
		Latitude: 30.50, Longitude: -97.70, LocationFixAt: t0.Add(-10 * time.Second)}, nil, m, ctx)

	path := s.drive.path
	end := path[len(path)-1]
	if end[0] != 30.51 || end[1] != -97.71 {
		t.Fatalf("stale replay must be rejected; path end = %v", end)
	}
	if s.drive.endLat != 30.51 {
		t.Fatalf("stale replay must not move endLat; got %v", s.drive.endLat)
	}
}
