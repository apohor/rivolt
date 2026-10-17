package rivian

import (
	"testing"
	"time"
)

// A frame that says both driving and charging (a stale gear, or charger
// fields stuck after an unplug) resolves to one of them by motion
// (flap-debounce.md §0.2): stationary means charging, moving means
// driving, and nothing closes and reopens on every frame.
func TestDriveChargeContradiction(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 10, 20, 20, 0, 0, time.UTC)
	frame := func(sec int, gear string, charging bool, mph, odoMi float64) State {
		st := State{At: t0.Add(time.Duration(sec) * time.Second), Gear: gear, SpeedKph: mph / kphToMi,
			OdometerKm: odoMi / kmToMi, BatteryLevelPct: 40 + float64(sec)/60, Latitude: 34.85, Longitude: -117.08,
			PowerState: "ready", ChargerState: "charger_disconnected", ChargerStatus: "chrgr_sts_not_connected"}
		if charging {
			st.ChargerState, st.ChargerStatus, st.ChargerPowerKW = "charging_active", "chrgr_sts_connected_charging", 150
		}
		return st
	}
	open := func(r *Replayer) (*liveCharge, *liveDrive) {
		r.m.sessMu.Lock()
		defer r.m.sessMu.Unlock()
		s := r.m.sessions[r.vehicle]
		if s == nil {
			return nil, nil
		}
		return s.charge, s.drive
	}

	t.Run("both in one frame, stationary: charging wins", func(t *testing.T) {
		r := NewReplayer("v")
		for i := 0; i < 120; i++ {
			r.Feed(frame(i, "D", true, 0, 1000))
		}
		c, d := open(r)
		if c == nil || !c.startedAt.Equal(t0) || d != nil || len(r.Drives()) != 0 {
			t.Errorf("charge %+v, drive %+v, closed drives %d: want one charge from the start, no drive", c, d, len(r.Drives()))
		}
	})

	t.Run("gear flipping D/P while charging, stationary", func(t *testing.T) {
		r := NewReplayer("v")
		for i := 0; i < 60; i++ {
			gear := "P"
			if i%2 == 1 {
				gear = "D"
			}
			r.Feed(frame(i, gear, true, 0, 1000))
		}
		c, d := open(r)
		if c == nil || !c.startedAt.Equal(t0) || d != nil || len(r.Drives()) != 0 {
			t.Errorf("charge %+v, drive %+v: want the first charge still open and no drive", c, d)
		}
	})

	t.Run("a real drive-off with stuck charger fields switches at once", func(t *testing.T) {
		r := NewReplayer("v")
		for i := 0; i < 30; i++ {
			r.Feed(frame(i*10, "P", true, 0, 1000))
		}
		// Unplugged and driving away, the charger fields still stuck.
		for s := 300; s < 330; s += 5 {
			r.Feed(frame(s, "D", true, 15, 1000.01+float64(s-300)*0.01))
		}
		c, d := open(r)
		if c != nil || d == nil || !d.startedAt.Equal(t0.Add(300*time.Second)) {
			t.Errorf("charge %+v, drive %+v: want no charge and the drive open from the first moving frame", c, d)
		}
	})

	t.Run("unplug then D: the drive opens at once", func(t *testing.T) {
		r := NewReplayer("v")
		for i := 0; i < 10; i++ {
			r.Feed(frame(i*10, "P", true, 0, 1000))
		}
		r.Feed(frame(100, "P", false, 0, 1000)) // unplugged
		r.Feed(frame(105, "D", false, 0, 1000))
		c, d := open(r)
		if c != nil || d == nil || !d.startedAt.Equal(t0.Add(105*time.Second)) {
			t.Errorf("charge %+v, drive %+v: want the drive open at the first D frame", c, d)
		}
	})
}
