package maps

import "math"

// SimplifyShape thins a (lat, lon) line with Douglas-Peucker: it keeps the first and last point and every point that sticks out more than
// tolM meters from the line drawn through the points kept, and drops the rest. A road's shape from the routing engine has a vertex every few
// meters; the map only needs the ones that bend the line. The result is always a subset of the input, in order, so it cannot leave the
// road's corridor by more than tolM. A line of fewer than three points, or a tolerance of zero or less, is returned as it is.
func SimplifyShape(shape [][2]float64, tolM float64) [][2]float64 {
	n := len(shape)
	if n < 3 || !(tolM > 0) {
		return shape
	}
	// Local flat-earth meters around the line's mean latitude (a leg is far smaller than the planet; the error is well under a meter).
	var sumLat float64
	for _, p := range shape {
		sumLat += p[0]
	}
	cosRef := math.Cos(sumLat / float64(n) * math.Pi / 180)
	const mPerDeg = 6371008.8 * math.Pi / 180
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i, p := range shape {
		xs[i] = p[1] * cosRef * mPerDeg
		ys[i] = p[0] * mPerDeg
	}
	keep := make([]bool, n)
	keep[0], keep[n-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, n - 1}} // explicit stack: a day's line has tens of thousands of points
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.b <= s.a+1 {
			continue
		}
		far, farD := -1, tolM
		for i := s.a + 1; i < s.b; i++ {
			if d := distToSegment(xs[i], ys[i], xs[s.a], ys[s.a], xs[s.b], ys[s.b]); d > farD {
				far, farD = i, d
			}
		}
		if far < 0 {
			continue
		}
		keep[far] = true
		stack = append(stack, span{s.a, far}, span{far, s.b})
	}
	out := make([][2]float64, 0, n/4+2)
	for i, k := range keep {
		if k {
			out = append(out, shape[i])
		}
	}
	return out
}

func distToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// SimplifyLegs simplifies each leg of a route at tolM, so every leg's two ends (the stops) are kept. A route's joined line counts a shared
// stop once (JoinLegs). If the joined line still has more than maxPoints, the tolerance doubles (up to eight times) until it fits; if even
// that is not enough, the legs are thinned evenly, ends kept. A cap below the number of legs plus one cannot hold the stops and is raised to it.
func SimplifyLegs(legs [][][2]float64, tolM float64, maxPoints int) [][][2]float64 {
	if min := len(legs) + 1; maxPoints < min {
		maxPoints = min
	}
	do := func(tol float64) ([][][2]float64, int) {
		out := make([][][2]float64, len(legs))
		total := 0
		for k, shape := range legs {
			out[k] = SimplifyShape(shape, tol)
			total += len(out[k])
			if k > 0 {
				total-- // the previous leg already ends at this stop
			}
		}
		return out, total
	}
	tol := tolM
	out, total := do(tol)
	for i := 0; i < 8 && total > maxPoints; i++ {
		tol *= 2
		out, total = do(tol)
	}
	if total <= maxPoints {
		return out
	}
	// Last resort: even thinning of each leg to its share, ends kept.
	share := (maxPoints - 1) / len(legs)
	if share < 1 {
		share = 1
	}
	for k, shape := range legs {
		out[k] = thinEvenly(shape, share+1)
	}
	return out
}

// JoinLegs is the route's line: the legs end to end, a stop shared by two legs once.
func JoinLegs(legs [][][2]float64) [][2]float64 {
	var line [][2]float64
	for k, shape := range legs {
		if k > 0 && len(shape) > 0 {
			shape = shape[1:]
		}
		line = append(line, shape...)
	}
	return line
}

// thinEvenly keeps at most n points of a line, evenly spaced by index, always the first and the last.
func thinEvenly(shape [][2]float64, n int) [][2]float64 {
	if n < 2 {
		n = 2
	}
	if len(shape) <= n {
		return shape
	}
	out := make([][2]float64, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, shape[i*(len(shape)-1)/(n-1)])
	}
	return out
}

// CheckShape is the routing guard for a line that was changed after it was checked: nil when the shape is on the globe and stays near the
// two ends it was asked to join, a *BadShapeError otherwise.
func CheckShape(shape [][2]float64, from, to [2]float64) error { return checkShape(shape, from, to) }
