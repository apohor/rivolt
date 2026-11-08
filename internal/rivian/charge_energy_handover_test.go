package rivian

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/charges"
)

// A charge's energy survives a handover. When a vehicle's lease moves to another pod, the new pod rebuilds the charge from
// the Redis snapshot; the snapshot used to carry the active seconds but not the energy integral, so every handover mid-charge
// restarted the charge's energy at zero (an L2 charge came out with only the energy since the last handover).
const chargeTestVehicle = "veh-1"

func TestChargeEnergySurvivesAHandover(t *testing.T) {
	ctx := context.Background()
	store := newMemLiveStateStore()
	at := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	mk := func() *StateMonitor {
		m := NewStateMonitor(nil, nil)
		m.SetLiveStateStore(store)
		m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T"}
		return m
	}
	// 7.5 kW, a minute apart, SoC rising a tenth of a percent a minute.
	push := func(m *StateMonitor, minute int) {
		m.applyVehicleStatePush(ctx, chargeTestVehicle, &State{VehicleID: chargeTestVehicle, At: at.Add(time.Duration(minute) * time.Minute),
			Gear: "P", ChargerState: "charging_active", ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: 7.5,
			BatteryLevelPct: 20 + float64(minute)/10, OdometerKm: 30000})
	}
	energy := func(m *StateMonitor) float64 {
		m.sessMu.Lock()
		defer m.sessMu.Unlock()
		return m.liveChargeRow(chargeTestVehicle, m.sessions[chargeTestVehicle].charge).EnergyAddedKWh
	}

	owner := mk()
	for minute := 0; minute <= 240; minute++ {
		push(owner, minute)
	}
	want := 7.5 * 4 // four hours at 7.5 kW
	if got := energy(owner); math.Abs(got-want) > 0.2 {
		t.Fatalf("setup: the first pod has %.2f kWh, want about %.1f", got, want)
	}

	// The lease moves: a second pod picks up from the snapshot and keeps charging for another two hours.
	peer := mk()
	for minute := 241; minute <= 360; minute++ {
		push(peer, minute)
	}
	want = 7.5 * 6
	if got := energy(peer); math.Abs(got-want) > 0.3 {
		t.Errorf("after the handover the charge has %.2f kWh, want about %.1f (the first pod's four hours must not be lost)", got, want)
	}
}

func TestChargeSnapshotCarriesEnergyAndOlderSnapshotsStillLoad(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	s := &liveSessions{charge: &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(time.Hour), energyIntKWh: 12.3, activeSeconds: 3600}}
	got := liveSessionsFromSnapshot(s.snapshot()).charge
	if got.energyIntKWh != 12.3 || got.progressKWh != 12.3 || got.activeSeconds != 3600 {
		t.Errorf("energy %.2f progress %.2f active %.0f after a round trip, want 12.3, 12.3, 3600", got.energyIntKWh, got.progressKWh, got.activeSeconds)
	}
	old := s.snapshot()
	old.Charge.EnergyIntKWh = 0 // a snapshot an older pod wrote
	if got := liveSessionsFromSnapshot(old).charge; got.energyIntKWh != 0 || got.activeSeconds != 3600 {
		t.Errorf("an older snapshot loaded as energy %.2f active %.0f", got.energyIntKWh, got.activeSeconds)
	}
}

func TestChargeEnergyFloor(t *testing.T) {
	const pack = 100.0
	for _, c := range []struct {
		name                     string
		energy, peak, start, end float64
		packKWh                  float64
		want                     float64
		floored                  bool
	}{
		{"a healthy AC charge is left alone", 36, 7.5, 20, 50, pack, 36, false},
		{"a little under the SoC energy is left alone", 20, 7.5, 20, 50, pack, 20, false},
		{"an integral that lost most of it is replaced, with the charging losses", 5, 7.5, 20, 50, pack, 32.4, true},
		{"just under the ratio is replaced", 17.9, 7.5, 20, 50, pack, 32.4, true},
		{"just over the ratio is left alone", 18.1, 7.5, 20, 50, pack, 18.1, false},
		{"no energy at all is replaced too", 0, 7.5, 20, 50, pack, 32.4, true},
		{"a DC session is left to its Parallax total", 5, 150, 20, 50, pack, 5, false},
		{"a small SoC step is not trusted", 0.1, 7.5, 20, 24, pack, 0.1, false},
		{"an unknown pack is left alone", 5, 7.5, 20, 50, 0, 5, false},
		{"no power seen is left alone", 5, 0, 20, 50, pack, 5, false},
		{"SoC falling is left alone", 5, 7.5, 50, 47, pack, 5, false},
		{"a start SoC of 0 (a failed seed) is not a 50 % gain", 5, 7.5, 0, 50, pack, 5, false},
		{"an end SoC over 100 is not trusted", 5, 7.5, 50, 140, pack, 5, false},
	} {
		got, floored := chargeEnergyFloor(c.energy, c.peak, c.start, c.end, c.packKWh)
		if math.Abs(got-c.want) > 1e-9 || floored != c.floored {
			t.Errorf("%s: got %.2f floored %v, want %.2f %v", c.name, got, floored, c.want, c.floored)
		}
	}
}

type floorCount struct{ n int }

func (f *floorCount) OnSleepEnded(string, time.Duration, bool) {}
func (f *floorCount) OnChargeEnergyFloor()                     { f.n++ }

// A floored charge is marked estimated, priced from the estimate, and counted once however often its row is rebuilt.
func TestFlooredChargeIsMarkedAndCountedOnce(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	obs := &floorCount{}
	m.sleepObserver = obs
	m.vehicleInfo[chargeTestVehicle] = &Vehicle{ID: chargeTestVehicle, Model: "R1T", PackKWh: 100}
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	c := &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 20, endSoC: 50, maxPower: 7.5,
		energyIntKWh: 5, activeSeconds: 4 * 3600, finalState: "charging_active"}
	// While the charge is open its row shows the live energy: not floored, not marked, not counted.
	if row := m.liveChargeRow(chargeTestVehicle, c); row.EnergyEstimated || row.EnergyAddedKWh != 5 || obs.n != 0 {
		t.Fatalf("an open row: estimated %v, %.2f kWh, counted %d; want the live 5 kWh, unmarked", row.EnergyEstimated, row.EnergyAddedKWh, obs.n)
	}
	// Closed (charging_complete, or the final write of a rejected charge) it is floored, marked and counted once.
	c.finalState = "charging_complete"
	for i := 0; i < 3; i++ {
		row := m.liveChargeRow(chargeTestVehicle, c)
		if !row.EnergyEstimated || math.Abs(row.EnergyAddedKWh-32.4) > 1e-9 {
			t.Fatalf("row %d: estimated %v, %.2f kWh, want true and 32.40", i, row.EnergyEstimated, row.EnergyAddedKWh)
		}
	}
	if obs.n != 1 {
		t.Errorf("counted %d times, want once", obs.n)
	}
	// A healthy charge is not marked and not counted.
	ok := &liveCharge{id: "d", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 20, endSoC: 50, maxPower: 7.5,
		energyIntKWh: 32, activeSeconds: 4 * 3600, finalState: "charging_complete"}
	if row := m.liveChargeRow(chargeTestVehicle, ok); row.EnergyEstimated || obs.n != 1 {
		t.Errorf("a healthy charge: estimated %v, counted %d", row.EnergyEstimated, obs.n)
	}
}

// A resumed charge's energy when Rivian's session total is available. The total counts per plug-in and restarts at a re-plug, so
// it must not replace what the stored row already holds: a re-plug inside the resume window adds to it, a restart or handoff of
// the same plug-in does not double count it, a lower reading never reduces it, and a second restart neither drops nor repeats the
// first part.
func TestResumedChargeEnergyAcrossPlugIns(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	lastWrite := t0.Add(5 * time.Hour)
	row := func(kwh float64) *charges.Charge {
		return &charges.Charge{ID: "live_v_c_1", VehicleID: "v", StartedAt: t0, EndedAt: lastWrite, StartSoCPct: 40, EndSoCPct: 70,
			EnergyAddedKWh: kwh, MaxPowerKW: 7, FinalState: "charging_active", Source: "live"}
	}
	sess := func(start time.Time, total float64) *LiveSession {
		st := ""
		if !start.IsZero() {
			st = start.Format(time.RFC3339Nano)
		}
		return &LiveSession{Active: true, PowerKW: 7, TotalChargedEnergyKWh: total, StartTime: st}
	}
	energy := func(m *StateMonitor, c *liveCharge, s *LiveSession) float64 {
		m.mu.Lock()
		m.lastSession["v"], m.lastSessionFor["v"] = s, c.id
		m.mu.Unlock()
		return m.liveChargeRow("v", c).EnergyAddedKWh
	}
	resume := func(kwh float64) (*StateMonitor, *liveCharge) {
		c := liveChargeFromRow(row(kwh))
		c.endAt, c.endSoC = lastWrite.Add(6*time.Hour), 80 // long enough that the physical energy ceiling does not bind
		return NewStateMonitor(nil, nil), c
	}

	// A re-plug inside the resume window: the new plug-in's total (20) is added to the stored 30.
	m, c := resume(30)
	if got := energy(m, c, sess(lastWrite.Add(10*time.Minute), 20)); got != 50 {
		t.Errorf("re-plug inside the window: %.1f kWh, want 30 + 20 = 50", got)
	}
	if got := energy(m, c, sess(lastWrite.Add(10*time.Minute), 26)); got != 56 {
		t.Errorf("the same new plug-in later: %.1f kWh, want 30 + 26 = 56", got)
	}

	// A restart of the same plug-in: the session began before the last write and its total already holds the stored 30.
	m, c = resume(30)
	if got := energy(m, c, sess(t0, 31)); got != 31 {
		t.Errorf("same-session restart: %.1f kWh, want the session's 31 (no double count)", got)
	}
	if got := energy(m, c, sess(t0, 35)); got != 35 {
		t.Errorf("the same session later: %.1f kWh, want 35", got)
	}

	// A lower reading never reduces the stored energy (same session, noise).
	m, c = resume(30)
	if got := energy(m, c, sess(t0, 29.5)); got < 30 {
		t.Errorf("a lower reading reduced the energy to %.1f kWh, want at least the stored 30", got)
	}
	// Unknown start and a total well below the stored energy is a reset: added.
	m, c = resume(30)
	if got := energy(m, c, sess(time.Time{}, 12)); got != 42 {
		t.Errorf("unknown start, lower total: %.1f kWh, want 30 + 12 = 42", got)
	}
	// Unknown start and a total at or above the stored energy is the same session.
	m, c = resume(30)
	if got := energy(m, c, sess(time.Time{}, 33)); got != 33 {
		t.Errorf("unknown start, higher total: %.1f kWh, want 33", got)
	}

	// A second restart of a row that already holds an earlier plug-in's energy (stored 50 = 30 + 20): the session total is 26, so the
	// row's energy beyond it (24) is carried; neither the first part is dropped nor is the session counted twice.
	m, c = resume(50)
	if got := energy(m, c, sess(t0.Add(5*time.Hour+10*time.Minute).Add(-time.Hour), 26)); got != 50 {
		t.Errorf("second restart: %.1f kWh, want the stored 50 kept", got)
	}
	if got := energy(m, c, sess(t0.Add(5*time.Hour+10*time.Minute).Add(-time.Hour), 31)); got != 55 {
		t.Errorf("second restart, session later: %.1f kWh, want 50 + 5 = 55", got)
	}

	// The live session lags a re-plug: the first build still sees the previous plug-in's session (same start, total 30, as the
	// row holds), then the new plug-in's (started after, 5 then 40). The base is decided again when the session changes, so the
	// first part is not lost: 30 + 5 = 35, then 30 + 40 = 70.
	m, c = resume(30)
	if got := energy(m, c, sess(t0, 30)); got != 30 {
		t.Errorf("lagging session, first build: %.1f kWh, want 30", got)
	}
	newPlug := lastWrite.Add(10 * time.Minute)
	if got := energy(m, c, sess(newPlug, 5)); got != 35 {
		t.Errorf("lagging session, the new plug-in appears: %.1f kWh, want 30 + 5 = 35", got)
	}
	if got := energy(m, c, sess(newPlug, 40)); got != 70 {
		t.Errorf("lagging session, the new plug-in later: %.1f kWh, want 30 + 40 = 70", got)
	}
	// The same session spelled differently by another feed is the same session: nothing is added twice.
	if got := energy(m, c, &LiveSession{Active: true, PowerKW: 7, TotalChargedEnergyKWh: 41, StartTime: newPlug.In(time.FixedZone("x", 3600)).Format(time.RFC3339)}); got != 71 {
		t.Errorf("the same session spelled in another zone: %.1f kWh, want 30 + 41 = 71", got)
	}
	// Plug-ins chain: a third session on top of the second.
	if got := energy(m, c, sess(lastWrite.Add(40*time.Minute), 6)); got != 77 {
		t.Errorf("a third plug-in: %.1f kWh, want 71 + 6 = 77", got)
	}
	// The feeds disagree during the lag: the old session reappears between builds of the new one. Its base is remembered, not
	// decided again from the grown energy, and the energy never goes down: old(30) → new(5) → old(30) → new(6) = 30, 35, 35, 36.
	m, c = resume(30)
	oldS, newS := t0, lastWrite.Add(10*time.Minute)
	for i, step := range []struct {
		start time.Time
		total float64
		want  float64
	}{{oldS, 30, 30}, {newS, 5, 35}, {oldS, 30, 35}, {newS, 6, 36}, {newS, 9, 39}} {
		if got := energy(m, c, sess(step.start, step.total)); got != step.want {
			t.Errorf("flip-flop step %d: %.1f kWh, want %.1f", i, got, step.want)
		}
	}

	// A fresh charge (not resumed) keeps today's rule: the total is the energy.
	fresh := &liveCharge{id: "live_v_c_9", startedAt: t0, endAt: t0.Add(4 * time.Hour), startSoC: 40, endSoC: 50, maxPower: 7, finalState: "charging_active"}
	if got := energy(NewStateMonitor(nil, nil), fresh, sess(t0, 20)); got != 20 {
		t.Errorf("a charge that was not resumed: %.1f kWh, want the session total 20", got)
	}
}

// A handoff keeps a resumed charge's energy baseline: without it the peer would drop back to "the session total is the energy".
func TestChargeSnapshotCarriesTheResumeBaseline(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 22, 0, 0, 0, time.UTC)
	s := &liveSessions{charge: &liveCharge{id: "c", startedAt: t0, endAt: t0.Add(time.Hour), baselineKWh: 30, baselineAt: t0.Add(time.Hour),
		sessionBases: []sessionBaseEntry{{Key: "2026-11-06T23:10:00Z", Base: 30}}, lastResumed: 35}}
	got := liveSessionsFromSnapshot(s.snapshot()).charge
	if got.baselineKWh != 30 || !got.baselineAt.Equal(t0.Add(time.Hour)) || len(got.sessionBases) != 1 || got.sessionBases[0].Key != "2026-11-06T23:10:00Z" || got.sessionBases[0].Base != 30 || got.lastResumed != 35 {
		t.Errorf("baseline %.1f at %v, session bases %v last %.1f after a round trip", got.baselineKWh, got.baselineAt, got.sessionBases, got.lastResumed)
	}
	old := s.snapshot()
	old.Charge.BaselineKWh, old.Charge.SessionBases = 0, nil // a snapshot an older pod wrote
	if got := liveSessionsFromSnapshot(old).charge; got.baselineKWh != 0 || len(got.sessionBases) != 0 {
		t.Errorf("an older snapshot loaded as a resumed charge")
	}
}
