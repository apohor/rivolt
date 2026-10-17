package rivian

import (
	"context"
	"math"
	"testing"
	"time"
)

// The public demo must never look frozen: on the sample feed no wall
// window passes without a frame for more than maxSilence, across a loop
// wrap or anywhere inside a loop, and the wrap continues the scene
// (same place, charge and plug state) rather than jumping.
func TestSampleFeedNeverGoesSilent(t *testing.T) {
	t.Parallel()
	const maxSilence = 15 * time.Minute
	f := NewSampleReplayFeed(nil, 0, nil)
	ctx := context.Background()
	tl := f.timeline
	wrap := tl.loopStart(3) // any loop boundary

	check := func(name string, from, to time.Time) []State {
		t.Helper()
		frames, err := f.frames(ctx, from, to)
		if err != nil || len(frames) == 0 {
			t.Fatalf("%s: frames: %d, %v", name, len(frames), err)
		}
		prev := from
		for _, fr := range frames {
			if gap := fr.At.Sub(prev); gap > maxSilence {
				t.Errorf("%s: %v without a frame before %s", name, gap.Round(time.Second), fr.At.Format(time.RFC3339))
			}
			prev = fr.At
		}
		if gap := to.Sub(prev); gap > maxSilence {
			t.Errorf("%s: %v without a frame at the end", name, gap.Round(time.Second))
		}
		return frames
	}

	// Across the wrap.
	frames := check("wrap", wrap.Add(-3*time.Hour), wrap.Add(3*time.Hour))
	var before, after *State
	for i := range frames {
		if frames[i].At.Before(wrap) {
			before = &frames[i]
		} else if after == nil {
			after = &frames[i]
		}
	}
	if before == nil || after == nil {
		t.Fatal("no frames on both sides of the wrap")
	}
	if math.Abs(before.BatteryLevelPct-after.BatteryLevelPct) > 1 || before.Gear != "P" || after.Gear != "P" ||
		haversineMeters(before.Latitude, before.Longitude, after.Latitude, after.Longitude) > 50 ||
		before.ChargerState != after.ChargerState {
		t.Errorf("the wrap jumps: %s %.1f%% %s at %.4f,%.4f → %s %.1f%% %s at %.4f,%.4f",
			before.Gear, before.BatteryLevelPct, before.ChargerState, before.Latitude, before.Longitude,
			after.Gear, after.BatteryLevelPct, after.ChargerState, after.Latitude, after.Longitude)
	}

	// A whole loop, in day-long windows (the sparsest stretches are
	// overnight sleeps).
	for d := tl.loopStart(3); d.Before(tl.loopStart(4)); d = d.Add(24 * time.Hour) {
		check("loop "+d.Format("2006-01-02"), d, d.Add(24*time.Hour))
	}

	// What "now" shows (latest) is never older than maxSilence.
	for w := wrap.Add(-30 * time.Minute); w.Before(wrap.Add(30 * time.Minute)); w = w.Add(time.Minute) {
		st, err := f.latest(ctx, w)
		if err != nil || st == nil {
			t.Fatalf("latest at %s: %v, %v", w, st, err)
		}
		if age := w.Sub(st.At); age > maxSilence {
			t.Errorf("latest at %s is %v old", w.Format(time.RFC3339), age.Round(time.Second))
		}
	}
}
