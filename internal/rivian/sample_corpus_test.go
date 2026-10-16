package rivian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// Same seed, same bytes (docs/product/sample-truck.md acceptance).
func TestSampleCorpus_Deterministic(t *testing.T) {
	t.Parallel()
	// A fresh build against the shared one (built the same way): same
	// seed, same bytes.
	var a, b bytes.Buffer
	if err := NewSampleCorpus(SampleSeed).WriteJSONL(&a); err != nil {
		t.Fatal(err)
	}
	if err := sharedSampleCorpus().WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(a.Bytes()) != sha256.Sum256(b.Bytes()) {
		t.Fatal("two runs with the same seed differ")
	}
	// Another seed plans a different month (its summary alone differs;
	// no need to render every frame).
	x, y := NewSampleCorpus(SampleSeed+1).Summary(), sharedSampleCorpus().Summary()
	if x.Frames == y.Frames && x.OdometerEndMi == y.OdometerEndMi {
		t.Error("a different seed produced the same corpus")
	}
}

// sharedSampleCorpus is the demo's corpus, built once per test binary.
// Tests only read it (Frames, Summary, segs), so they can share it and run
// in parallel; building it is most of each sample test's cost.
func sharedSampleCorpus() *SampleCorpus {
	c, _ := sampleShared()
	return c
}

var (
	sampleReplayOnce sync.Once
	sampleReplay     *Replayer
)

// sharedSampleReplay is the whole shared corpus fed once through the real
// recorder (the slowest step under -race); tests read its drives and
// charges.
func sharedSampleReplay() *Replayer {
	sampleReplayOnce.Do(func() {
		c := sharedSampleCorpus()
		start, end := c.span()
		frames, _ := c.Frames(context.Background(), start, end.Add(time.Second))
		sampleReplay = NewReplayer("sample")
		sampleReplay.FeedAll(frames)
	})
	return sampleReplay
}

// The corpus has what the spec asks for.
func TestSampleCorpus_Shape(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	s := c.Summary()
	t.Logf("summary: %+v", s)
	if d := s.End.Sub(s.Start); d < 44*24*time.Hour || d > 46*24*time.Hour {
		t.Errorf("span = %s, want ~45 days", d)
	}
	if s.HomeL2 < 15 || s.HomeL2 > 25 {
		t.Errorf("home L2 plug-ins = %d, want ~20", s.HomeL2)
	}
	if s.TopUps < s.HomeL2/2 {
		t.Errorf("top-ups = %d for %d plug-ins, want 1–3 on most", s.TopUps, s.HomeL2)
	}
	if s.DCFC != 4 || s.AwayAC != 3 || s.FailedSleeps != 3 {
		t.Errorf("dcfc %d away AC %d failed sleeps %d, want 4 / 3 / 3", s.DCFC, s.AwayAC, s.FailedSleeps)
	}
	if len(s.RoadTripMiles) != 2 || s.RoadTripMiles[0] < 600 || s.RoadTripMiles[1] < 600 {
		t.Errorf("road trips = %v mi, want two of 600+", s.RoadTripMiles)
	}
	if s.MinSoC < 5 || s.MaxSoC > 95 {
		t.Errorf("SoC range %.1f–%.1f", s.MinSoC, s.MaxSoC)
	}
}

// Frames are strictly ordered, the odometer never runs backwards, and
// LastAtOrBefore agrees with Frames.
func TestSampleCorpus_FramesConsistent(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	start, end := c.span()
	all, err := c.Frames(context.Background(), start, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(all); i++ {
		if !all[i].At.After(all[i-1].At) {
			t.Fatalf("frame %d at %s not after %s", i, all[i].At, all[i-1].At)
		}
		if all[i].OdometerKm < all[i-1].OdometerKm-1e-6 {
			t.Fatalf("odometer went backwards at %s", all[i].At)
		}
	}
	for _, at := range []time.Time{start, start.Add(37 * time.Hour), start.Add(19*24*time.Hour + 13*time.Minute), end} {
		got, _ := c.LastAtOrBefore(context.Background(), at)
		window, _ := c.Frames(context.Background(), start, at.Add(time.Nanosecond))
		if got == nil || !got.At.Equal(window[len(window)-1].At) {
			t.Errorf("LastAtOrBefore(%s) = %v, want %s", at, got, window[len(window)-1].At)
		}
	}
	if got, _ := c.LastAtOrBefore(context.Background(), start.Add(-time.Second)); got != nil {
		t.Error("frame before the corpus start")
	}
}

// Through the real recorder: every planned drive comes out as a drive,
// with no phantom or runaway trips.
func TestSampleCorpus_ThroughRecorder(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	r := sharedSampleReplay()
	drives := r.Drives()
	want := c.Summary().Drives
	if len(drives) < want*9/10 || len(drives) > want {
		t.Errorf("recorded drives = %d, planned %d", len(drives), want)
	}
	for _, d := range drives {
		if d.DistanceMi > 400 || d.DistanceMi <= 0 {
			t.Errorf("drive %s distance %.1f mi", d.ID, d.DistanceMi)
		}
	}
	// Every planned DC fast charge is recorded, at its station.
	fast := 0
	for _, ch := range r.Charges() {
		if !IsDCFCSession(ch.MaxPowerKW, ch.EnergyAddedKWh, ch.ActiveSeconds, ch.EndedAt.Sub(ch.StartedAt)) {
			continue
		}
		fast++
		at := false
		for _, st := range sampleDCFCStations {
			at = at || haversineMeters(ch.Lat, ch.Lon, st.at.Lat, st.at.Lon) <= 250
		}
		if !at || ch.FinalState == "abandoned" {
			t.Errorf("fast charge %s at %.5f,%.5f final %q: want closed normally at a station", ch.ID, ch.Lat, ch.Lon, ch.FinalState)
		}
	}
	if fast != c.Summary().DCFC {
		t.Errorf("recorded fast charges = %d, planned %d", fast, c.Summary().DCFC)
	}
	// The Rivian-billed stop (RAN Barstow) records Rivian's price; the
	// others are left to the network-estimate pricer (none in a replay).
	billed := 0
	for _, ch := range r.Charges() {
		if !IsDCFCSession(ch.MaxPowerKW, ch.EnergyAddedKWh, ch.ActiveSeconds, ch.EndedAt.Sub(ch.StartedAt)) {
			continue
		}
		atRAN := haversineMeters(ch.Lat, ch.Lon, sampleDCFCStations[0].at.Lat, sampleDCFCStations[0].at.Lon) <= 250
		switch {
		case atRAN && (ch.CostSource != CostSourceRivian || ch.Currency != "USD" || ch.Cost <= 0 ||
			math.Abs(ch.Cost/ch.EnergyAddedKWh-0.48) > 0.05 || ch.Network != "Rivian Adventure Network"):
			t.Errorf("RAN charge %s: %s %.2f %s over %.1f kWh, network %q, want Rivian-billed at ~$0.48/kWh by Rivian Adventure Network",
				ch.ID, ch.CostSource, ch.Cost, ch.Currency, ch.EnergyAddedKWh, ch.Network)
		case atRAN:
			billed++
		case ch.CostSource == CostSourceRivian:
			t.Errorf("non-RAN charge %s billed by Rivian", ch.ID)
		}
	}
	if billed != 1 {
		t.Errorf("Rivian-billed fast charges = %d, want 1 (Barstow)", billed)
	}
}

// Every DC fast charge sits on one of the real stations, each of the
// three is used, and each prices as a known network (not "unmatched"),
// so the demo's network estimates resolve.
func TestSampleCorpusDCFCAtRealStations(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	used := map[int]int{}
	for _, s := range c.segs {
		if s.kind != segCharge || !s.dcfc {
			continue
		}
		found := -1
		for i, st := range sampleDCFCStations {
			if haversineMeters(s.path[0].Lat, s.path[0].Lon, st.at.Lat, st.at.Lon) <= 1 {
				found = i
			}
		}
		if found < 0 {
			t.Errorf("DC fast charge at %+v isn't at a sample station", s.path[0])
			continue
		}
		used[found]++
	}
	if len(used) != len(sampleDCFCStations) {
		t.Errorf("stations used %v, want all %d", used, len(sampleDCFCStations))
	}
	for _, st := range sampleDCFCStations {
		n := dcfcrates.MatchNetwork(dcfcrates.StationQuery(st.network, st.name))
		if n.Slug == dcfcrates.UnmatchedNetwork.Slug {
			t.Errorf("%s (%s) doesn't match a priced network", st.name, st.network)
		}
	}
}

// SampleUserPrice finds the one hand-priced session (the Vegas hotel L2)
// wherever a loop of the replay puts it on the wall clock, and nothing
// else.
func TestSampleUserPrice(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	r := sharedSampleReplay()
	tl := sampleTimeline(c)
	for _, loop := range []int64{0, 3, 17} {
		shift := tl.loopStart(loop).Sub(tl.start)
		var hits []string
		for _, ch := range r.Charges() {
			cost, cur, ok := SampleUserPrice(ch.StartedAt.Add(shift))
			if !ok {
				continue
			}
			hits = append(hits, ch.ID)
			if cost != 15 || cur != "USD" || haversineMeters(ch.Lat, ch.Lon, 36.1699, -115.1398) > 500 ||
				IsDCFCSession(ch.MaxPowerKW, ch.EnergyAddedKWh, ch.ActiveSeconds, ch.EndedAt.Sub(ch.StartedAt)) {
				t.Errorf("loop %d: %s priced %.2f %s at %.4f,%.4f: want the Vegas hotel AC at $15", loop, ch.ID, cost, cur, ch.Lat, ch.Lon)
			}
		}
		if len(hits) != 1 {
			t.Errorf("loop %d: hand-priced sessions %v, want exactly one", loop, hits)
		}
	}
}

// The sample truck reports as often as a real one (prod, 7 days to
// 2026-10-15): within 3 min while asleep (the connection TTL's replay),
// within 5 s while awake, parked, plugged in or charging, so the demo's
// Now card never looks stale next to a live truck.
func TestSampleCorpusCadence(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	start, end := c.span()
	frames, err := c.Frames(context.Background(), start, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var worstAsleep, worstAwake time.Duration
	for i := 1; i < len(frames); i++ {
		gap := frames[i].At.Sub(frames[i-1].At)
		if frames[i-1].PowerState == "sleep" {
			worstAsleep = max(worstAsleep, gap)
		} else {
			worstAwake = max(worstAwake, gap)
		}
	}
	if worstAsleep > sampleAsleepStep || worstAwake > sampleAwakeStep {
		t.Errorf("largest gaps: asleep %v (want ≤ %v), awake %v (want ≤ %v)", worstAsleep, sampleAsleepStep, worstAwake, sampleAwakeStep)
	}
}

// Every leg the sample truck drives follows a road: it has a generated
// shape (no straight-line fallback; the real guarantee), and as a
// backstop no drive has a straight stretch over 50 km. I-5 through the
// Central Valley runs straight for ~30 km, kept whole by the 20-m
// simplification; the old waypoint diagonals ran 130-230 km.
func TestSampleRoutesFollowRoads(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	for _, l := range c.legs {
		if _, ok := sampleRouteShapes[routeKey(l[0], l[1])]; !ok {
			t.Errorf("leg %s has no road shape; regenerate sample_routes_gen.go", routeKey(l[0], l[1]))
		}
	}
	for _, s := range c.segs {
		if s.kind != segDrive {
			continue
		}
		for i := 1; i < len(s.path); i++ {
			if d := haversineMeters(s.path[i-1].Lat, s.path[i-1].Lon, s.path[i].Lat, s.path[i].Lon); d > 50000 {
				t.Errorf("drive at %s: a %.1f km straight stretch", s.start.Format(time.RFC3339), d/1000)
				break
			}
		}
	}
}

// Plugged in, the sample truck never sits past its own charge limit: a
// top-up restores what the park drained, so a car "holding 90%" reads
// 90%, not 91%. (Unplugged it may: the limit drops back to 80 for the
// next night after a 90% trip charge.)
func TestSampleCorpusSoCWithinLimit(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	start, end := c.span()
	frames, err := c.Frames(context.Background(), start, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	over := 0
	for _, f := range frames {
		plugged := f.ChargerState == "charging_complete" || f.ChargerState == "charging_active"
		if plugged && f.ChargeTargetPct > 0 && f.BatteryLevelPct > f.ChargeTargetPct+0.05 {
			if over < 5 {
				t.Errorf("%s: SoC %.1f%% over the %.0f%% limit (%s)", f.At.Format(time.RFC3339), f.BatteryLevelPct, f.ChargeTargetPct, f.ChargerState)
			}
			over++
		}
	}
	if over > 0 {
		t.Errorf("%d frames over the charge limit", over)
	}
}

// The Truck tab shows the sample's software version, tires and sleep:
// every frame reports a version (none pending) and the tire pressure.
func TestSampleCorpus_TruckFields(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	s := c.Summary()
	frames, err := c.Frames(context.Background(), s.Start, s.Start.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	asleep := 0
	for _, f := range frames {
		if f.OtaCurrentVersion != sampleSoftwareVersion || f.OtaAvailableVersion != "" {
			t.Fatalf("frame at %v: software %q / %q", f.At, f.OtaCurrentVersion, f.OtaAvailableVersion)
		}
		if f.TirePressureFLBar <= 0 || f.TirePressureRRBar <= 0 {
			t.Fatalf("frame at %v: no tire pressure", f.At)
		}
		if f.PowerState == "sleep" {
			asleep++
		}
	}
	if asleep == 0 {
		t.Error("no sleep in two days")
	}
}

// The Truck tab's idle-awake (samples.SleepActivity: parked, not
// charging, not asleep; intervals capped at 20 min) stays in line with
// a healthy R1's: the sample's parks go up ~13 min, standby 1 min, then
// sleep (fleet p50s), so a typical day is about an hour. The failed-sleep
// night's three retries stay up 20 min each by design (the "woke up N
// times" story), which is most of the max.
func TestSampleCorpus_IdleAwakePerDay(t *testing.T) {
	t.Parallel()
	c := sharedSampleCorpus()
	s := c.Summary()
	fr, err := c.Frames(context.Background(), s.Start, s.End.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	idle := map[time.Time]float64{}
	viaStandby, sleeps := 0, 0
	for i := 0; i+1 < len(fr); i++ {
		f := fr[i]
		if f.PowerState == "sleep" && i > 0 && fr[i-1].PowerState != "sleep" {
			sleeps++
			if fr[i-1].PowerState == "standby" {
				viaStandby++
			}
		}
		d := math.Min(fr[i+1].At.Sub(f.At).Seconds(), 1200)
		cs := strings.ToLower(f.ChargerState)
		if f.PowerState != "sleep" && f.Gear != "D" && f.Gear != "R" && f.Gear != "N" &&
			cs != "charging_active" && cs != "charging_connecting" {
			idle[f.At.Truncate(24*time.Hour)] += d / 60
		}
	}
	var mins []float64
	for _, m := range idle {
		mins = append(mins, m)
	}
	sort.Float64s(mins)
	if med, top := mins[len(mins)/2], mins[len(mins)-1]; med < 30 || med > 70 || top > 130 {
		t.Errorf("idle-awake per day: median %.0f min, max %.0f min; want about an hour, at most ~2 h", med, top)
	}
	// Every sleep but the failed-sleep night's retries comes after a
	// standby minute, as on the fleet.
	if sleeps-viaStandby > 3 {
		t.Errorf("%d of %d sleeps came straight from ready", sleeps-viaStandby, sleeps)
	}
}
