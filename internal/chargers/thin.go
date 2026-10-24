package chargers

import "math"

// MaxRoutePoints is the longest route polyline an API caller may send. A long drive's polyline has tens of
// thousands of points (a 1,300 km route is about 25,000), far over MaxPathPoints; the handler thins it first.
const MaxRoutePoints = 200_000

// ThinPath returns path with points dropped so that at most max remain, keeping the first and the last and spacing
// the rest evenly along the route, so the shape follows the road to within about the spacing (a few hundred metres on
// a continental route, against a corridor of kilometres). A path that is already short enough, or has a coordinate
// that is not a number (QueryCorridor refuses that), is returned as it is.
func ThinPath(path [][2]float64, max int) [][2]float64 {
	if max < 3 || len(path) <= max {
		return path
	}
	cum := make([]float64, len(path))
	for i := 1; i < len(path); i++ {
		d := haversineM(path[i-1][0], path[i-1][1], path[i][0], path[i][1])
		if math.IsNaN(d) || math.IsInf(d, 0) {
			return path
		}
		cum[i] = cum[i-1] + d
	}
	total := cum[len(cum)-1]
	out := make([][2]float64, 0, max)
	out = append(out, path[0])
	if total > 0 {
		step := total / float64(max-2)
		next := step
		for i := 1; i < len(path)-1; i++ {
			if cum[i] >= next {
				out = append(out, path[i])
				next = cum[i] + step
			}
		}
	}
	return append(out, path[len(path)-1])
}
