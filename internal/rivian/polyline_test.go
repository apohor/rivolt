package rivian

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"
)

// A drive's stored route must never hold a point that is not on the globe: it draws as two segments of thousands of kilometres, out and back,
// on the drive's map, its thumbnail and the Activity map. Plainly fake places near (10, 10).

func TestEncodePolylineSkipsPointsOffTheGlobe(t *testing.T) {
	good := [][2]float64{{10.000, 10.000}, {10.001, 10.001}, {10.002, 10.002}}
	withBad := [][2]float64{
		{10.000, 10.000}, {100, -970}, {10.001, 10.001}, {math.NaN(), 10}, {10, math.Inf(1)}, {10.002, 10.002}, {-91, 0}, {0, 181},
	}
	if got, want := encodePolyline(withBad), encodePolyline(good); got != want {
		t.Fatalf("encode with bad points = %q, want the same as without them (%q)", got, want)
	}
	for _, p := range decodeSamplePolyline(encodePolyline(withBad)) {
		if math.Abs(p.Lat) > 90 || math.Abs(p.Lon) > 180 {
			t.Fatalf("a stored point is off the globe: %v", p)
		}
	}
}

func TestEncodePolylineRoundTripsRealPoints(t *testing.T) {
	pts := [][2]float64{{10.000, 10.000}, {10.00123, 9.99877}, {-33.5, 151.25}, {89.9, -179.9}}
	got := decodeSamplePolyline(encodePolyline(pts))
	if len(got) != len(pts) {
		t.Fatalf("decoded %d points, want %d", len(got), len(pts))
	}
	for i, p := range pts {
		if math.Abs(got[i].Lat-p[0]) > 1e-5 || math.Abs(got[i].Lon-p[1]) > 1e-5 {
			t.Errorf("point %d = %v, want %v", i, got[i], p)
		}
	}
}

type fakeFiller struct {
	shape [][2]float64
	err   error
	calls int
}

func (f *fakeFiller) RouteShape(context.Context, [2]float64, [2]float64) ([][2]float64, error) {
	f.calls++
	return f.shape, f.err
}

// A hop of a few hundred metres after a 60 s gap is filled from the routing engine. If the shape it hands back has a vertex off the globe
// (it was read at the wrong precision), the fill is refused and the path gets only the real fix: a straight line, never a corrupt point.
func TestAppendDrivePathRefusesAFillWithAPointOffTheGlobe(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from, to := [2]float64{10.000, 10.000}, [2]float64{10.003, 10.003}
	d := &liveDrive{path: [][2]float64{from}, lastFixAt: t0}
	f := &fakeFiller{shape: [][2]float64{from, {100.25, -970.1}, to}}
	m := &StateMonitor{logger: slog.Default(), routeFiller: f}
	m.appendDrivePath(context.Background(), "veh-1", d, to[0], to[1], t0.Add(time.Minute))
	if f.calls != 1 {
		t.Fatalf("the filler was asked %d times, want 1", f.calls)
	}
	if len(d.path) != 2 || d.path[1] != to {
		t.Fatalf("path = %v, want the start and the real fix only", d.path)
	}
}

func TestAppendDrivePathKeepsAGoodFill(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from, to, mid := [2]float64{10.000, 10.000}, [2]float64{10.003, 10.003}, [2]float64{10.0025, 10.001}
	d := &liveDrive{path: [][2]float64{from}, lastFixAt: t0}
	m := &StateMonitor{logger: slog.Default(), routeFiller: &fakeFiller{shape: [][2]float64{from, mid, to}}}
	m.appendDrivePath(context.Background(), "veh-1", d, to[0], to[1], t0.Add(time.Minute))
	if len(d.path) != 3 || d.path[1] != mid || d.path[2] != to {
		t.Fatalf("path = %v, want start, the fill's middle vertex, then the fix", d.path)
	}
}

func TestAppendDrivePathIgnoresAFixOffTheGlobe(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	from := [2]float64{10.000, 10.000}
	d := &liveDrive{path: [][2]float64{from}, lastFixAt: t0}
	m := &StateMonitor{logger: slog.Default(), routeFiller: &fakeFiller{err: errors.New("unused")}}
	for _, p := range [][2]float64{{100, 10}, {10, 400}, {math.NaN(), 10}, {10, math.Inf(-1)}} {
		m.appendDrivePath(context.Background(), "veh-1", d, p[0], p[1], t0.Add(time.Minute))
	}
	if len(d.path) != 1 {
		t.Fatalf("path = %v, want only the real start", d.path)
	}
}

func TestValidCoord(t *testing.T) {
	for _, c := range []struct {
		lat, lon float64
		want     bool
	}{{10, 10, true}, {0, 0, true}, {90, 180, true}, {-90, -180, true}, {90.0001, 0, false}, {0, -180.5, false}, {math.NaN(), 0, false}, {0, math.Inf(1), false}} {
		if got := validCoord(c.lat, c.lon); got != c.want {
			t.Errorf("validCoord(%v, %v) = %v, want %v", c.lat, c.lon, got, c.want)
		}
	}
}
