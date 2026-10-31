package drives

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// The route thumbnails of the drive list: a drive's stored route cut down to a few points, enough to draw its shape as a tiny SVG.

// ThumbMaxPoints is the most points a thumbnail carries (endpoints included).
const ThumbMaxPoints = 24

// RouteStub is what a thumbnail needs of a drive: its id, when it started (for the free plan's history window) and its stored route.
type RouteStub struct {
	ID            string
	StartedAt     time.Time
	RoutePolyline string
}

// RoutesByIDs returns the user's drives among ids (external ids), each with its stored route; an id the user does not have, or that has
// no route, is simply absent. rivianVehicleID, when set, keeps one truck's (the free plan's one-vehicle limit). It reads only this user's
// rows, like every other read of the store.
func (s *Store) RoutesByIDs(ctx context.Context, ids []string, rivianVehicleID string) ([]RouteStub, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// One placeholder per id (a handful at most; the caller caps them), after $1 the user and $2 the truck.
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, s.userID, rivianVehicleID)
	for i, id := range ids {
		marks[i] = "$" + strconv.Itoa(i+3)
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.external_id, d.started_at, COALESCE(d.route_polyline, '')
		FROM drives d
		JOIN vehicles v ON v.id = d.vehicle_id
		WHERE d.user_id = $1
		  AND d.external_id IN (`+strings.Join(marks, ",")+`)
		  AND ($2::text = '' OR v.rivian_vehicle_id = $2::text)
		  AND COALESCE(d.route_polyline, '') <> ''`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteStub
	for rows.Next() {
		var r RouteStub
		if err := rows.Scan(&r.ID, &r.StartedAt, &r.RoutePolyline); err != nil {
			return nil, err
		}
		r.StartedAt = r.StartedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ThumbPolyline cuts a stored route (a precision-5 polyline) to at most ThumbMaxPoints points that keep its shape, as a polyline again. Empty
// when the route has fewer than two points.
func ThumbPolyline(encoded string) string {
	pts := decodePolyline5(encoded)
	if len(pts) < 2 {
		return ""
	}
	return encodePolyline5(simplify(pts, ThumbMaxPoints))
}

// simplify keeps at most max points of pts, always the first and last, by Douglas-Peucker with the smallest tolerance that gets under max (found
// by bisection), so the shape survives and a long straight run costs two points.
func simplify(pts [][2]float64, max int) [][2]float64 {
	if len(pts) <= max {
		return pts
	}
	lo, hi := 0.0, extent(pts)
	best := dp(pts, hi)
	for i := 0; i < 24; i++ {
		mid := (lo + hi) / 2
		if r := dp(pts, mid); len(r) <= max {
			best, hi = r, mid
		} else {
			lo = mid
		}
	}
	// Bisection can leave a few over on a tie; a final even thinning guarantees the bound.
	if len(best) > max {
		out := make([][2]float64, 0, max)
		for i := 0; i < max; i++ {
			out = append(out, best[i*(len(best)-1)/(max-1)])
		}
		return out
	}
	return best
}

func extent(pts [][2]float64) float64 {
	minLa, maxLa, minLo, maxLo := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		minLa, maxLa = math.Min(minLa, p[0]), math.Max(maxLa, p[0])
		minLo, maxLo = math.Min(minLo, p[1]), math.Max(maxLo, p[1])
	}
	return math.Max(maxLa-minLa, maxLo-minLo)
}

// dp is Douglas-Peucker on (lat, lon) as a plane (a thumbnail's shape does not need the projection), iteratively so a long route cannot
// overflow the stack.
func dp(pts [][2]float64, eps float64) [][2]float64 {
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.b <= s.a+1 {
			continue
		}
		far, farD := -1, eps
		for i := s.a + 1; i < s.b; i++ {
			if d := segDist(pts[i], pts[s.a], pts[s.b]); d > farD {
				far, farD = i, d
			}
		}
		if far >= 0 {
			keep[far] = true
			stack = append(stack, span{s.a, far}, span{far, s.b})
		}
	}
	out := make([][2]float64, 0, len(pts))
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/(dx*dx+dy*dy)))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

var errPolyline = errors.New("malformed polyline")

// decodePolyline5 decodes a Google-format polyline at precision 5; a malformed tail ends the route there.
func decodePolyline5(s string) [][2]float64 {
	var out [][2]float64
	var lat, lon int
	i := 0
	next := func() (int, error) {
		result, shift := 0, 0
		for {
			if i >= len(s) {
				return 0, errPolyline
			}
			b := int(s[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
			if shift > 30 {
				return 0, errPolyline
			}
		}
		if result&1 != 0 {
			return ^(result >> 1), nil
		}
		return result >> 1, nil
	}
	for i < len(s) {
		dlat, err := next()
		if err != nil {
			break
		}
		dlon, err := next()
		if err != nil {
			break
		}
		lat += dlat
		lon += dlon
		out = append(out, [2]float64{float64(lat) / 1e5, float64(lon) / 1e5})
	}
	return out
}

func encodePolyline5(pts [][2]float64) string {
	var b strings.Builder
	var pLat, pLon int
	put := func(v int) {
		u := v << 1
		if v < 0 {
			u = ^u
		}
		for u >= 0x20 {
			b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
			u >>= 5
		}
		b.WriteByte(byte(u + 63))
	}
	for _, p := range pts {
		la, lo := int(math.Round(p[0]*1e5)), int(math.Round(p[1]*1e5))
		put(la - pLat)
		put(lo - pLon)
		pLat, pLon = la, lo
	}
	return b.String()
}
