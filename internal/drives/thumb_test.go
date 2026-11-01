package drives

import (
	"math"
	"testing"
)

// A made-up wiggly route near (10, 10): n points along a sine-ish path, about 0.2 degrees across.
func wiggle(n int) [][2]float64 {
	out := make([][2]float64, n)
	for i := range out {
		t := float64(i) / float64(n-1)
		out[i] = [2]float64{10 + 0.2*t, 10 + 0.05*math.Sin(t*6*math.Pi)}
	}
	return out
}

func TestThumbKeepsTheShapeInFewPoints(t *testing.T) {
	src := wiggle(900)
	got := decodePolyline5(ThumbPolyline(encodePolyline5(src)))
	if len(got) < 2 || len(got) > ThumbMaxPoints {
		t.Fatalf("%d points, want 2..%d", len(got), ThumbMaxPoints)
	}
	first, last := src[0], src[len(src)-1]
	if math.Abs(got[0][0]-first[0]) > 6e-5 || math.Abs(got[0][1]-first[1]) > 6e-5 || math.Abs(got[len(got)-1][0]-last[0]) > 6e-5 || math.Abs(got[len(got)-1][1]-last[1]) > 6e-5 {
		t.Errorf("endpoints moved: %v ... %v, want %v ... %v", got[0], got[len(got)-1], first, last)
	}
	// Every original point lies close to the simplified line: within 5% of the route's extent.
	tol := 0.05 * extent(src)
	for _, p := range src {
		best := math.Inf(1)
		for i := 0; i+1 < len(got); i++ {
			best = math.Min(best, segDist(p, got[i], got[i+1]))
		}
		if best > tol {
			t.Fatalf("a point is %v from the thumbnail, over %v: the shape was lost", best, tol)
		}
	}
}

func TestShortRoutesAreKeptAndStraightRunsCostTwoPoints(t *testing.T) {
	short := wiggle(10)
	if got := decodePolyline5(ThumbPolyline(encodePolyline5(short))); len(got) != 10 {
		t.Errorf("a 10-point route came back with %d points", len(got))
	}
	straight := make([][2]float64, 500)
	for i := range straight {
		straight[i] = [2]float64{10 + float64(i)*0.0001, 10 + float64(i)*0.0001}
	}
	if got := decodePolyline5(ThumbPolyline(encodePolyline5(straight))); len(got) != 2 {
		t.Errorf("a straight route came back with %d points, want 2", len(got))
	}
}

func TestThumbOfNothingOrGarbageIsEmptyNeverAPanic(t *testing.T) {
	for _, in := range []string{"", "!", "~~~~~~~~~~~~~~~~~~~~~~~~", "_p~iF", "\x00\x01\x02"} {
		if got := ThumbPolyline(in); got != "" {
			// A one-point or malformed route has no shape to draw.
			if pts := decodePolyline5(got); len(pts) < 2 {
				t.Errorf("ThumbPolyline(%q) = %q, a route of %d points", in, got, len(pts))
			}
		}
	}
}

func TestPolylineRoundTrip(t *testing.T) {
	src := [][2]float64{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}}
	got := decodePolyline5(encodePolyline5(src))
	if len(got) != 3 {
		t.Fatalf("%v", got)
	}
	for i := range src {
		if math.Abs(got[i][0]-src[i][0]) > 1e-5 || math.Abs(got[i][1]-src[i][1]) > 1e-5 {
			t.Errorf("point %d: %v, want %v", i, got[i], src[i])
		}
	}
	// The reference example from Google's documentation.
	if enc := encodePolyline5(src); enc != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Errorf("encoded %q", enc)
	}
}

// The answer carries no more precision than a thumbnail needs: every point is a multiple of 1e-4 degrees (about 11 m).
func TestThumbPointsAreSnappedToElevenMetres(t *testing.T) {
	for _, p := range decodePolyline5(ThumbPolyline(encodePolyline5(wiggle(900)))) {
		for _, v := range p {
			if r := math.Abs(v*1e4 - math.Round(v*1e4)); r > 1e-6 {
				t.Fatalf("%v is not a multiple of 1e-4 (off by %v)", v, r)
			}
		}
	}
}

// A stored route can hold a point off the globe (a corrupt route-fill, since refused when a route is built). The thumbnail drops it, so the
// picture is the road and not two lines shooting off and back. Plainly fake places near (10, 10).
func TestThumbDropsAStoredPointOffTheGlobe(t *testing.T) {
	good := [][2]float64{{10.000, 10.000}, {10.001, 10.001}, {10.002, 10.002}, {10.003, 10.003}}
	withBad := [][2]float64{{10.000, 10.000}, {10.001, 10.001}, {100.25, -970.1}, {10.002, 10.002}, {10.003, 10.003}}
	want := ThumbPolyline(encodePolyline5(good))
	got := ThumbPolyline(encodePolyline5(withBad))
	if got == "" || got != want {
		t.Fatalf("thumb with a bad point = %q, want the same as without it (%q)", got, want)
	}
	for _, p := range decodePolyline5(got) {
		if math.Abs(p[0]) > 90 || math.Abs(p[1]) > 180 {
			t.Fatalf("a thumbnail point is off the globe: %v", p)
		}
	}
}
