package rivian

import (
	"testing"
	"time"
)

// A wake from sleep makes the first vehicleState frame replay the charger state from before the
// sleep ("charging_active" on a truck that finished at 80 % hours ago); the next frame, about a
// second later, says complete. The session that opened and closed in that second moved nothing and
// must not become a charge: in prod that made 238 of the 240 charges under 20 s, one per wake on a
// plugged-in truck, hidden by the lists (under a minute) but still counted and priced. The
// persistence side is in charge_blip_integration_test.go; this pins the bounds.
func TestLiveChargeBlipBounds(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	for _, c := range []struct {
		name  string
		dur   time.Duration
		d     float64
		blip  bool
		extra string
	}{
		{"same tick, no change (the old guard)", 0, 0, true, ""},
		{"1.6 s, flat", 1600 * time.Millisecond, 0, true, ""},
		{"19 s, flat", 19 * time.Second, 0, true, ""},
		{"20 s, flat", 20 * time.Second, 0, false, "the bound is exclusive"},
		{"1.6 s, moved 0.5", 1600 * time.Millisecond, 0.5, false, ""},
		{"1.6 s, moved 0.4", 1600 * time.Millisecond, 0.4, true, ""},
		{"8 s, dropped 0.6", 8 * time.Second, -0.6, false, "a drop that big is not nothing either"},
	} {
		lc := &liveCharge{startedAt: t0, endAt: t0.Add(c.dur), startSoC: 70, endSoC: 70 + c.d}
		if got := lc.blip(); got != c.blip {
			t.Errorf("%s: blip=%v, want %v %s", c.name, got, c.blip, c.extra)
		}
	}
}
