package rivian

import (
	"testing"
	"time"
)

// The charge limit the truck reports is kept on the charge: the last sane value of the
// session, not cleared by a frame without one or spoiled by an out-of-range one, and
// carried through a restart from the stored row.
func TestChargeLimitIsRecordedOnTheCharge(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	frame := func(sec int, cs string, kw, soc, limit float64) State {
		return State{At: t0.Add(time.Duration(sec) * time.Second), Gear: "P", ChargerState: cs,
			ChargerStatus: "chrgr_sts_connected_charging", ChargerPowerKW: kw, BatteryLevelPct: soc,
			ChargeTargetPct: limit, OdometerKm: 30000, Latitude: 34.16, Longitude: -118.17, PowerState: "ready"}
	}
	t.Run("last sane value wins; 0 and out-of-range frames leave it", func(t *testing.T) {
		r := NewReplayer("v")
		r.Feed(frame(0, "charging_active", 11, 40, 90))
		r.Feed(frame(60, "charging_active", 11, 45, 0))    // no limit in this frame
		r.Feed(frame(120, "charging_active", 11, 50, 80))  // the owner lowered it
		r.Feed(frame(180, "charging_active", 11, 55, 250)) // nonsense
		r.Feed(frame(240, "charging_active", 11, 60, 0))
		r.Feed(frame(300, "charging_complete", 0, 80, 0))
		cs := r.Charges()
		if len(cs) != 1 || cs[0].ChargeLimitPct == nil || *cs[0].ChargeLimitPct != 80 {
			t.Fatalf("charges = %+v, want one with a limit of 80", cs)
		}
	})
	t.Run("a charge that never reported one has none", func(t *testing.T) {
		r := NewReplayer("v")
		r.Feed(frame(0, "charging_active", 11, 40, 0))
		r.Feed(frame(300, "charging_complete", 0, 80, 0))
		cs := r.Charges()
		if len(cs) != 1 || cs[0].ChargeLimitPct != nil {
			t.Fatalf("charges = %+v, want one with no limit (null, not 0)", cs)
		}
	})
}

func TestNoteLimitAndRowRoundTrip(t *testing.T) {
	c := &liveCharge{}
	for _, v := range []float64{0, -5, 101, 250} {
		c.noteLimit(v)
	}
	if c.limitPct != 0 {
		t.Fatalf("limit = %v after only bad values", c.limitPct)
	}
	c.noteLimit(1)
	c.noteLimit(100)
	if c.limitPct != 100 {
		t.Errorf("limit = %v, want the last good value 100", c.limitPct)
	}
	c.noteLimit(0)
	if c.limitPct != 100 {
		t.Errorf("a frame without a limit cleared it: %v", c.limitPct)
	}
	// Through the stored row and back (a restart mid-charge).
	m := NewStateMonitor(nil, nil)
	start := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	c2 := &liveCharge{id: "c", startedAt: start, endAt: start.Add(time.Hour), startSoC: 30, endSoC: 60, maxPower: 11, energyIntKWh: 20, finalState: "charging_active", limitPct: 85}
	row := m.liveChargeRow("veh", c2)
	if row.ChargeLimitPct == nil || *row.ChargeLimitPct != 85 {
		t.Fatalf("row limit = %v", row.ChargeLimitPct)
	}
	if back := liveChargeFromRow(&row); back.limitPct != 85 {
		t.Errorf("rebuilt limit = %v, want 85", back.limitPct)
	}
	c2.limitPct = 0
	if row := m.liveChargeRow("veh", c2); row.ChargeLimitPct != nil {
		t.Errorf("a charge with no limit wrote %v", *row.ChargeLimitPct)
	}
}
