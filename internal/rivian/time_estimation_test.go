package rivian

import (
	"context"
	"testing"
	"time"
)

func TestDecodeTimeEstimation(t *testing.T) {
	for name, tc := range map[string]struct {
		b64  string
		mins float64
		ok   bool
	}{
		"minutes only":          {pbVarints(2, 288), 288, true},
		"validity then minutes": {pbVarints(1, 1, 2, 2), 2, true},
		"zero":                  {pbVarints(2, 0), 0, true},
		"no minutes field":      {pbVarints(1, 1), 0, false},
		"implausible":           {pbVarints(2, 999999), 0, false},
		"not base64":            {"%%%", 0, false},
	} {
		if m, ok := decodeTimeEstimation(tc.b64); m != tc.mins || ok != tc.ok {
			t.Errorf("%s: got %v %v, want %v %v", name, m, ok, tc.mins, tc.ok)
		}
	}
}

// An R2 charging: vehicleState sends no time to full, Parallax's countdown
// (captured Oct 17-18: 288 down to 2 minutes) supplies it, marked estimate; the
// vehicle's own value wins, and the end of the charge clears it.
func TestR2TimeToFullFromParallax(t *testing.T) {
	ctx := context.Background()
	vs := func(r *r2Run, cs string, kw, toEnd float64) {
		r.m.applyVehicleStatePush(ctx, "r2", &State{VehicleID: "r2", At: time.Now(), Gear: "P",
			ChargerState: cs, ChargerPowerKW: kw, TimeToEndMins: toEnd, BatteryLevelPct: 60, OdometerKm: 30000})
	}
	px := func(r *r2Run, mins uint64, at time.Time) {
		r.m.onDriveDynamicsFrame(ctx, "r2", DriveDynamicsFrame{RVM: rvmTimeEstimation,
			Payload: pbVarints(2, mins), TimestampMs: at.UnixMilli()})
	}

	t.Run("countdown while charging", func(t *testing.T) {
		r := newR2Run()
		vs(r, "charging_active", 7.4, 0)
		now := time.Now()
		for i, m := range []uint64{288, 240, 130, 31, 2} {
			px(r, m, now.Add(time.Duration(i)*time.Minute))
			if c := r.cached(); c.TimeToEndMins != float64(m) || c.TimeToEndSource != "estimate" {
				t.Fatalf("after %d: %v (%q)", m, c.TimeToEndMins, c.TimeToEndSource)
			}
		}
		vs(r, "charging_active", 7.4, 0) // a vehicleState push without the field keeps it
		if c := r.cached(); c.TimeToEndMins != 2 || c.TimeToEndSource != "estimate" {
			t.Errorf("a push blanked the estimate: %v (%q)", c.TimeToEndMins, c.TimeToEndSource)
		}
		vs(r, "charging_complete", 0, 0)
		if c := r.cached(); c.TimeToEndMins != 0 || c.TimeToEndSource != "" {
			t.Errorf("the estimate outlived the charge: %v (%q)", c.TimeToEndMins, c.TimeToEndSource)
		}
	})

	t.Run("not while idle", func(t *testing.T) {
		r := newR2Run()
		vs(r, "charging_ready", 0, 0)
		px(r, 120, time.Now())
		if c := r.cached(); c.TimeToEndMins != 0 || c.TimeToEndSource != "" {
			t.Errorf("an idle plugged truck got %v (%q)", c.TimeToEndMins, c.TimeToEndSource)
		}
	})

	t.Run("an older frame doesn't apply", func(t *testing.T) {
		r := newR2Run()
		vs(r, "charging_active", 7.4, 0)
		now := time.Now()
		px(r, 100, now)
		px(r, 200, now.Add(-time.Hour))
		if c := r.cached(); c.TimeToEndMins != 100 {
			t.Errorf("got %v, want 100", c.TimeToEndMins)
		}
	})

	t.Run("the vehicle's own value wins", func(t *testing.T) {
		r := newR2Run()
		vs(r, "charging_active", 7.4, 95)
		px(r, 120, time.Now())
		if c := r.cached(); c.TimeToEndMins != 95 || c.TimeToEndSource != "" {
			t.Errorf("got %v (%q), want vehicleState's 95", c.TimeToEndMins, c.TimeToEndSource)
		}
		r2 := newR2Run()
		vs(r2, "charging_active", 7.4, 0)
		px(r2, 120, time.Now())
		vs(r2, "charging_active", 7.4, 80) // then vehicleState starts reporting
		if c := r2.cached(); c.TimeToEndMins != 80 || c.TimeToEndSource != "" {
			t.Errorf("got %v (%q), want 80 unmarked", c.TimeToEndMins, c.TimeToEndSource)
		}
	})
}
