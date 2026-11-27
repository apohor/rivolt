package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// The public demo needs no extra data for the Now card's "Battery while parked" graph (design now_parked_graph): the generated sample truck
// already sleeps, wakes and parks long enough. This pins that: fed through the same series builder as the route, the sample corpus gives a
// drawn graph (points, at least one sleep bar) at every hour of its run, and wake-ups at nearly all of them (the failed-sleep night and the
// evenings away keep them from being every hour). If the generator changes so a window comes back empty, the demo's Now loses the graph, so this fails.
func TestSampleTruckFeedsTheParkedGraph(t *testing.T) {
	c := rivian.NewSampleCorpus(rivian.SampleSeed)
	sum := c.Summary()
	frames, err := c.Frames(context.Background(), sum.Start, sum.End)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]parkedRow, 0, len(frames))
	for _, s := range frames {
		kind := "p"
		if s.Gear == "D" || s.Gear == "R" || s.Gear == "N" || s.SpeedKph >= 1.6 {
			kind = "d"
		}
		if s.ChargerState == "charging_active" || s.ChargerState == "charging_connecting" {
			kind = "c"
		}
		soc, rng := s.BatteryLevelPct, s.DistanceToEmpty/1.609344
		rows = append(rows, parkedRow{At: s.At, SoC: &soc, RangeMi: &rng, Sleep: s.PowerState == "sleep", Kind: kind})
	}
	hours, noSleep, wakes, parkedNow := 0, 0, 0, 0
	lo := 0
	for now := sum.Start.Add(parkedSeriesWindow); now.Before(sum.End); now = now.Add(time.Hour) {
		from := now.Add(-parkedSeriesWindow)
		for lo < len(rows) && rows[lo].At.Before(from) {
			lo++
		}
		hi := lo
		for hi < len(rows) && !rows[hi].At.After(now) {
			hi++
		}
		out := buildParkedSeries(rows[lo:hi], now, time.UTC)
		hours++
		if len(out.Points) == 0 {
			t.Fatalf("no graph at %s", now.Format(time.RFC3339))
		}
		if len(out.Sleeps) == 0 {
			noSleep++
		}
		if len(out.Wakes) > 0 {
			wakes++
		}
		if out.Parked {
			parkedNow++
		}
	}
	if noSleep != 0 {
		t.Errorf("%d of %d hourly windows have no sleep bar", noSleep, hours)
	}
	if wakes*100 < hours*90 {
		t.Errorf("wake-ups in %d of %d windows, want at least 90%%", wakes, hours)
	}
	if parkedNow*100 < hours*50 {
		t.Errorf("parked at %d of %d hours, want most of them", parkedNow, hours)
	}
}
