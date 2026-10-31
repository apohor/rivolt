package routegaps

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// Points around 0.1 degrees of latitude apart are ~11 km; 0.0045 is ~500 m; 0.001 is ~111 m. Latitudes only, so distances are exact.
func line(lats ...float64) [][2]float64 {
	out := make([][2]float64, len(lats))
	for i, l := range lats {
		out[i] = [2]float64{l, -100.0}
	}
	return out
}

type fakeFiller struct {
	calls  int
	fail   bool
	shape  func(from, to [2]float64) [][2]float64
	delays time.Duration
}

func (f *fakeFiller) RouteShape(ctx context.Context, from, to [2]float64) ([][2]float64, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("valhalla down")
	}
	if f.shape != nil {
		return f.shape(from, to), nil
	}
	mid1 := [2]float64{from[0] + (to[0]-from[0])/3, from[1] + 0.001}
	mid2 := [2]float64{from[0] + 2*(to[0]-from[0])/3, from[1] + 0.001}
	return [][2]float64{from, mid1, mid2, to}, nil
}

func TestDecodeRoundTripsTheStoredFormat(t *testing.T) {
	in := [][2]float64{{37.77491, -122.41942}, {37.77512, -122.41901}, {37.8044, -122.2712}, {-33.8688, 151.2093}}
	got, err := Decode(rivian.EncodePolyline(in))
	if err != nil || len(got) != len(in) {
		t.Fatalf("%v %d points, want %d", err, len(got), len(in))
	}
	for i := range in {
		if math.Abs(got[i][0]-in[i][0]) > 1e-5 || math.Abs(got[i][1]-in[i][1]) > 1e-5 {
			t.Errorf("point %d = %v, want %v", i, got[i], in[i])
		}
	}
	for _, bad := range []string{"\x01", "_p~iF~ps|U_ulLnnqC_"} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("%q decoded without an error", bad)
		}
	}
	if pts, err := Decode(""); err != nil || len(pts) != 0 {
		t.Errorf("empty route: %v %v", pts, err)
	}
}

func TestGapsAreOnlyDropoutsNotNormalSpacingOrJumps(t *testing.T) {
	pts := line(30.000, 30.001, 30.002, 30.0075, 30.0076, 30.2, 30.2001) // 111 m, 111 m, ~611 m, 11 m, ~21 km, 11 m
	if got := Gaps(pts, DefaultMinGapM); len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Fatalf("gaps = %v, want segments 2 and 4", got)
	}
	// A teleport is not a dropout.
	if got := Gaps(line(30.0, 31.0), DefaultMinGapM); len(got) != 0 {
		t.Errorf("a ~111 km jump counted as a dropout: %v", got)
	}
	if got := Gaps(line(30.0), DefaultMinGapM); len(got) != 0 {
		t.Errorf("one point: %v", got)
	}
}

func TestFillSplicesTheShapeLikeTheRecorderDoes(t *testing.T) {
	pts := line(30.000, 30.001, 30.010, 30.011) // one dropout between the 2nd and 3rd points (~1 km)
	f := &fakeFiller{}
	res := Fill(context.Background(), pts, f, DefaultMinGapM)
	if res.Gaps != 1 || res.Filled != 1 || res.Failed != 0 || f.calls != 1 {
		t.Fatalf("%+v, %d calls", res, f.calls)
	}
	// 4 original points + the shape's 2 inner vertices; the shape's endpoints are the fixes already there.
	if len(res.Points) != 6 || res.Points[1] != pts[1] || res.Points[4] != pts[2] || res.Points[5] != pts[3] {
		t.Fatalf("points = %v", res.Points)
	}
	if got := Gaps(res.Points, DefaultMinGapM); len(got) != 0 {
		t.Errorf("a ~1 km dropout filled by a 3-leg shape still has gaps: %v", got)
	}
	// Nothing to fill: the input comes back unchanged and no call is made.
	f2 := &fakeFiller{}
	if res := Fill(context.Background(), line(30.000, 30.001), f2, DefaultMinGapM); res.Gaps != 0 || f2.calls != 0 || len(res.Points) != 2 {
		t.Errorf("no dropout: %+v, %d calls", res, f2.calls)
	}
}

func TestFillLeavesWhatValhallaCannotFillAndCapsTheCalls(t *testing.T) {
	pts := line(30.000, 30.010)
	if res := Fill(context.Background(), pts, &fakeFiller{fail: true}, DefaultMinGapM); res.Filled != 0 || res.Failed != 1 || len(res.Points) != 2 {
		t.Errorf("valhalla down: %+v", res)
	}
	short := &fakeFiller{shape: func(from, to [2]float64) [][2]float64 { return [][2]float64{from, to} }}
	if res := Fill(context.Background(), pts, short, DefaultMinGapM); res.Filled != 0 || res.Failed != 1 {
		t.Errorf("a shape with no inner vertex: %+v", res)
	}
	var many [][2]float64
	for i := 0; i < MaxFillsPerDrive+5; i++ { // MaxFillsPerDrive+5 points make MaxFillsPerDrive+4 dropouts
		many = append(many, [2]float64{30 + float64(i)*0.01, -100})
	}
	f := &fakeFiller{}
	res := Fill(context.Background(), many, f, DefaultMinGapM)
	if f.calls != MaxFillsPerDrive || res.Filled != MaxFillsPerDrive || res.Failed != 4 || res.Gaps != MaxFillsPerDrive+4 {
		t.Errorf("cap: %d calls, %+v", f.calls, res)
	}
}

func TestPlanCountsOnlyDrivesWithDropouts(t *testing.T) {
	enc := func(p [][2]float64) string { return rivian.EncodePolyline(p) }
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	oct, nov := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	p := MakePlan([]Candidate{
		{ID: a, StartedAt: oct, Polyline: enc(line(30.000, 30.010, 30.011))},
		{ID: b, StartedAt: oct, Polyline: enc(line(30.000, 30.001, 30.002))},
		{ID: c, StartedAt: nov, Polyline: enc(line(30.000, 30.010, 30.020))},
		{ID: d, StartedAt: nov, Polyline: "\x01"},
	}, DefaultMinGapM)
	if p.Scanned != 4 || len(p.Drives) != 2 || p.GapsTotal != 3 || p.Undecoded != 1 {
		t.Fatalf("%+v", p)
	}
	if m := p.ByMonth(); m["2026-10"] != 1 || m["2026-11"] != 1 {
		t.Errorf("by month = %v", m)
	}
	ids := p.IDs()
	if len(ids) != 2 || Digest(ids) != Digest([]uuid.UUID{ids[1], ids[0]}) {
		t.Error("the digest depends on order")
	}
	if Digest(ids) == Digest(ids[:1]) {
		t.Error("the digest ignores membership")
	}
}

func TestValhallaHostMustBeThisDeploymentsOwn(t *testing.T) {
	for _, ok := range []string{
		"http://localhost:8002", "http://127.0.0.1:8002", "http://[::1]:8002", "http://valhalla.maps.svc.cluster.local:8002",
		"http://valhalla.maps.svc:8002", "http://10.43.1.2:8002", "http://192.168.1.20:8002", "http://172.16.4.4", "http://[fd12:3456::1]:8002",
		"https://VALHALLA.maps.svc.cluster.local",
	} {
		if err := CheckValhallaURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"https://valhalla1.openstreetmap.de", "http://valhalla.example.com:8002", "http://8.8.8.8:8002", "http://[2606:4700::1111]:8002",
		"http://172.32.0.1", "http://localhost.evil.example", "http://valhalla.svc.cluster.local.evil.example", "http://evil.example/localhost",
		"ftp://localhost", "localhost:8002", "", "http://", "http://user@public.example",
	} {
		if err := CheckValhallaURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
