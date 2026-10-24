package chargers

import (
	"math"
	"testing"
)

// A winding road: a long sine over 1,300 km, 30,000 points.
func windingRoute(n int) [][2]float64 {
	p := make([][2]float64, n)
	for i := range p {
		f := float64(i) / float64(n-1)
		p[i] = [2]float64{33 + 6*f + 0.5*math.Sin(f*40), -97 - 8*f}
	}
	return p
}

func TestThinPathKeepsTheShape(t *testing.T) {
	in := windingRoute(30_000)
	out := ThinPath(in, MaxPathPoints)
	if len(out) > MaxPathPoints || len(out) < MaxPathPoints/2 {
		t.Fatalf("%d points, want at most %d", len(out), MaxPathPoints)
	}
	if out[0] != in[0] || out[len(out)-1] != in[len(in)-1] {
		t.Error("the ends must be kept")
	}
	// Every original point is within 300 m of the thinned line.
	pp := projectPath(out)
	worst := 0.0
	for i := 0; i < len(in); i += 7 {
		if d := pp.perpDistanceM(in[i][0], in[i][1], 0, false); !math.IsInf(d, 1) && d > worst { // Inf: the point projects past an end
			worst = d
		}
	}
	if worst > 300 {
		t.Errorf("thinned line strays %.0f m from the route", worst)
	}
	// The scan thins a long path itself, and takes the thinned one as it is.
	for name, p := range map[string][][2]float64{"long": in, "thinned": out} {
		if got, _, err := planCorridor(p, 10); err != nil || len(got) > MaxPathPoints {
			t.Errorf("%s path: %d points kept, %v", name, len(got), err)
		}
	}
}

func TestThinPathLeavesShortAndBadPathsAlone(t *testing.T) {
	short := windingRoute(100)
	if got := ThinPath(short, MaxPathPoints); len(got) != 100 {
		t.Errorf("short path changed: %d", len(got))
	}
	bad := windingRoute(6000)
	bad[10][0] = math.NaN()
	if got := ThinPath(bad, MaxPathPoints); len(got) != 6000 {
		t.Errorf("a path with NaN must be returned as is for the planner to refuse, got %d", len(got))
	}
	same := make([][2]float64, 9000) // all one point: no length
	got := ThinPath(same, 5000)
	if len(got) != 2 {
		t.Errorf("zero-length path: %d points", len(got))
	}
}
