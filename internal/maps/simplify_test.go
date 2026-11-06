package maps

import (
	"errors"
	"math"
	"testing"
)

// wiggle is a synthetic road: n vertices from (lat0, 10) northwards for about km kilometres, with a gentle sideways wobble of amplitude
// ampM meters (a road's own curves) and nothing real in it.
func wiggle(lat0, km float64, n int, ampM float64) [][2]float64 {
	out := make([][2]float64, n)
	for i := range out {
		f := float64(i) / float64(n-1)
		lat := lat0 + km/111.195*f
		lon := 10 + ampM/111195*math.Sin(f*40*math.Pi)
		out[i] = [2]float64{lat, lon}
	}
	return out
}

// devM is how far p is from the line a-b (flat-earth meters at the reference latitude).
func devM(p, a, b [2]float64) float64 {
	cos := math.Cos(p[0] * math.Pi / 180)
	const m = 111195.0
	return distToSegment(p[1]*cos*m, p[0]*m, a[1]*cos*m, a[0]*m, b[1]*cos*m, b[0]*m)
}

func nearestDev(p [2]float64, line [][2]float64) float64 {
	best := math.Inf(1)
	for i := 1; i < len(line); i++ {
		if d := devM(p, line[i-1], line[i]); d < best {
			best = d
		}
	}
	return best
}

func TestSimplifyShapeDropsPointsAndStaysInTolerance(t *testing.T) {
	in := wiggle(10, 600, 60_000, 400) // about 600 km, a vertex every 10 m
	out := SimplifyShape(in, 10)
	if len(out) >= len(in)/10 {
		t.Fatalf("%d points in, %d out: not thinned enough", len(in), len(out))
	}
	if out[0] != in[0] || out[len(out)-1] != in[len(in)-1] {
		t.Error("the ends of the line moved")
	}
	for i := 0; i < len(in); i += 37 {
		if d := nearestDev(in[i], out); d > 10.5 {
			t.Fatalf("point %d is %.1f m from the simplified line, tolerance is 10", i, d)
		}
	}
	// A subset, in order.
	j := 0
	for _, p := range out {
		for j < len(in) && in[j] != p {
			j++
		}
		if j == len(in) {
			t.Fatal("the simplified line has a point that is not in the original, or out of order")
		}
	}
}

func TestSimplifyShapeEdgeCases(t *testing.T) {
	straight := wiggle(10, 50, 5000, 0)
	if got := SimplifyShape(straight, 10); len(got) != 2 {
		t.Errorf("a straight line keeps %d points, want its 2 ends", len(got))
	}
	corner := [][2]float64{{10, 10}, {10.1, 10}, {10.1, 10.1}} // a right angle, kilometres wide
	if got := SimplifyShape(corner, 10); len(got) != 3 {
		t.Errorf("a corner was cut: %v", got)
	}
	short := [][2]float64{{10, 10}, {10.1, 10.1}}
	for _, tol := range []float64{0, -5, math.NaN()} {
		if got := SimplifyShape(wiggle(10, 5, 100, 50), tol); len(got) != 100 {
			t.Errorf("tolerance %v changed the line to %d points", tol, len(got))
		}
	}
	if got := SimplifyShape(short, 10); len(got) != 2 {
		t.Errorf("two points became %d", len(got))
	}
	if got := SimplifyShape(nil, 10); len(got) != 0 {
		t.Errorf("nil became %v", got)
	}
	same := [][2]float64{{10, 10}, {10, 10}, {10, 10}, {10, 10}} // zero-length segment: no division by zero
	if got := SimplifyShape(same, 10); len(got) < 2 {
		t.Errorf("a stationary line lost its ends: %v", got)
	}
}

// Every leg keeps both ends (the stops), the joined line stays under the cap even when the tolerance alone would not do it, and a cap
// too small for the stops is raised to hold them.
func TestSimplifyLegsKeepsStopsAndRespectsTheCap(t *testing.T) {
	var legs [][][2]float64
	for k := 0; k < 4; k++ {
		legs = append(legs, wiggle(10+float64(k)*3, 250, 20_000, 3000))
		// legs must join end to end: shift each so its first point is the previous leg's last
		if k > 0 {
			prev := legs[k-1][len(legs[k-1])-1]
			d := [2]float64{prev[0] - legs[k][0][0], prev[1] - legs[k][0][1]}
			for i := range legs[k] {
				legs[k][i][0] += d[0]
				legs[k][i][1] += d[1]
			}
		}
	}
	for _, cap := range []int{4000, 400, 40, 2} {
		out := SimplifyLegs(legs, 10, cap)
		min := len(legs) + 1
		want := cap
		if want < min {
			want = min
		}
		line := JoinLegs(out)
		if len(line) > want {
			t.Errorf("cap %d: %d points", cap, len(line))
		}
		for k := range legs {
			if out[k][0] != legs[k][0] || out[k][len(out[k])-1] != legs[k][len(legs[k])-1] {
				t.Errorf("cap %d: leg %d lost a stop", cap, k)
			}
		}
	}
}

func TestCheckShapeStillRefusesAnOffRouteLine(t *testing.T) {
	from, to := [2]float64{10, 10}, [2]float64{10.5, 10}
	var bad *BadShapeError
	if err := CheckShape([][2]float64{from, {40, 60}, to}, from, to); !errors.As(err, &bad) || bad.Kind != "detour" {
		t.Errorf("a vertex thousands of km away: %v", err)
	}
	if err := CheckShape([][2]float64{from, {95, 10}, to}, from, to); !errors.As(err, &bad) || bad.Kind != "offglobe" {
		t.Errorf("a vertex off the globe: %v", err)
	}
	if err := CheckShape(wiggle(10, 50, 100, 50), from, [2]float64{10.45, 10}); err != nil {
		t.Errorf("a real shape was refused: %v", err)
	}
}
