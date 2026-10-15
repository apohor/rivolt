package rivian

import "fmt"

// Road-following sample routes.
//
// The sample planner drives between a handful of public landmarks; a
// straight line between them drew as a diagonal across the map. Each
// waypoint-to-waypoint leg it drives has a road shape, precomputed once
// with the routing service (sample_routes_gen_test.go, run by hand) and
// checked in as sampleRouteShapes, so generating the corpus stays
// offline and deterministic. A leg without a shape falls back to the
// straight line with sampleRoadRatio.

// sampleRoad expands waypoints into the road path and its length in
// miles.
func sampleRoad(way []samplePoint) ([]samplePoint, float64) {
	path := []samplePoint{way[0]}
	miles := 0.0
	for i := 1; i < len(way); i++ {
		from, to := way[i-1], way[i]
		if enc, ok := sampleRouteShapes[routeKey(from, to)]; ok {
			pts := decodeSamplePolyline(enc)
			if len(pts) >= 2 {
				// The road shape starts and ends where the road meets
				// the waypoints; join them to the exact points.
				pts[0], pts[len(pts)-1] = from, to
				path = append(path, pts[1:]...)
				miles += pathMiles(pts)
				continue
			}
		}
		path = append(path, to)
		miles += pathMiles([]samplePoint{from, to}) * sampleRoadRatio
	}
	return path, miles
}

// routeKey identifies a leg by its endpoints, to 1e-6 degrees.
func routeKey(a, b samplePoint) string {
	return fmt.Sprintf("%.6f,%.6f>%.6f,%.6f", a.Lat, a.Lon, b.Lat, b.Lon)
}

// decodeSamplePolyline decodes a Google encoded polyline (precision 5),
// the inverse of encodePolyline.
func decodeSamplePolyline(s string) []samplePoint {
	var out []samplePoint
	lat, lon := 0, 0
	for i := 0; i < len(s); {
		for k := 0; k < 2; k++ {
			shift, result := 0, 0
			for i < len(s) {
				b := int(s[i]) - 63
				i++
				result |= (b & 0x1f) << shift
				shift += 5
				if b < 0x20 {
					break
				}
			}
			d := result >> 1
			if result&1 != 0 {
				d = ^d
			}
			if k == 0 {
				lat += d
			} else {
				lon += d
			}
		}
		out = append(out, samplePoint{float64(lat) / 1e5, float64(lon) / 1e5})
	}
	return out
}
