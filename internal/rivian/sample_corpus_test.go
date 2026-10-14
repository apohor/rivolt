package rivian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// Same seed, same bytes (docs/product/sample-truck.md acceptance).
func TestSampleCorpus_Deterministic(t *testing.T) {
	var a, b bytes.Buffer
	if err := NewSampleCorpus(SampleSeed).WriteJSONL(&a); err != nil {
		t.Fatal(err)
	}
	if err := NewSampleCorpus(SampleSeed).WriteJSONL(&b); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(a.Bytes()) != sha256.Sum256(b.Bytes()) {
		t.Fatal("two runs with the same seed differ")
	}
	var c bytes.Buffer
	_ = NewSampleCorpus(SampleSeed + 1).WriteJSONL(&c)
	if bytes.Equal(a.Bytes(), c.Bytes()) {
		t.Error("a different seed produced the same corpus")
	}
}

// The corpus has what the spec asks for.
func TestSampleCorpus_Shape(t *testing.T) {
	c := NewSampleCorpus(SampleSeed)
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
	c := NewSampleCorpus(SampleSeed)
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
	c := NewSampleCorpus(SampleSeed)
	start, end := c.span()
	frames, _ := c.Frames(context.Background(), start, end.Add(time.Second))
	r := NewReplayer("sample")
	r.FeedAll(frames)
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
}

// Every DC fast charge sits on one of the real stations, each of the
// three is used, and each prices as a known network (not "unmatched"),
// so the demo's network estimates resolve.
func TestSampleCorpusDCFCAtRealStations(t *testing.T) {
	c := NewSampleCorpus(SampleSeed)
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
