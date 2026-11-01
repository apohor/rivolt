package rivian

import (
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
)

// encodePolyline encodes a slice of (lat, lon) points using Google's
// polyline algorithm at precision 5 (~1.1m resolution). The result is
// the same format consumed by Leaflet's various polyline-decoding
// helpers and by Google Maps; see
// https://developers.google.com/maps/documentation/utilities/polylinealgorithm
//
// We use it to persist a drive's GPS path on the drives row so the
// overview map can render a real on-road route instead of a straight
// line between start/end markers.
//
// The input is expected to be an already-thinned trace (one point per
// recorder frame). At ~1 frame / few seconds and 5-decimal varint
// encoding this comes out to well under 10 KB even for multi-hour
// drives.
// EncodePolyline is encodePolyline for repair tools that rebuild a
// drive's route from stored samples.
func EncodePolyline(points [][2]float64) string { return encodePolyline(points) }

func encodePolyline(points [][2]float64) string {
	if len(points) == 0 {
		return ""
	}
	var b strings.Builder
	// Each point worst-case is 11 chars (5+1 per coord delta).
	b.Grow(len(points) * 8)
	var prevLat, prevLon int32
	for _, p := range points {
		if !validCoord(p[0], p[1]) {
			noteRoutePointRefused("encode_skip", "")
			continue // never persist a point that is not on the globe: it would draw two huge segments out and back
		}
		lat := int32(roundHalfEven(p[0] * 1e5))
		lon := int32(roundHalfEven(p[1] * 1e5))
		encodeSigned(&b, lat-prevLat)
		encodeSigned(&b, lon-prevLon)
		prevLat, prevLon = lat, lon
	}
	return b.String()
}

// roundHalfEven rounds to the nearest integer with banker's rounding,
// matching the reference implementation closely enough for the
// 1e-5-degree quantisation we need.
func roundHalfEven(f float64) float64 {
	if f >= 0 {
		return float64(int64(f + 0.5))
	}
	return float64(int64(f - 0.5))
}

func encodeSigned(b *strings.Builder, v int32) {
	u := uint32(v) << 1
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
		u >>= 5
	}
	b.WriteByte(byte(u + 63))
}

// validCoord reports whether (lat, lon) is a real position on the globe: finite and within +-90 / +-180. (0, 0) is a valid coordinate here;
// callers treat "no fix" separately.
func validCoord(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180
}

// routePointObserver hears every route point the recorder or the encoder refuses to put into a stored route, with where it was refused:
// "fix_offglobe" (a position off the globe), "fill_globe" (a route-fill with such a vertex, caught by the recorder), "fill_offglobe" and
// "fill_detour" (the routing engine's shape failed its own check: off the globe, or nowhere near the two ends), "encode_skip" (the
// encoder's last line). Set at startup by SetRoutePointObserver; nil counts nothing.
var routePointObserver atomic.Pointer[func(where string)]

// SetRoutePointObserver wires the counter for refused route points (rivolt_route_point_refused_total{where}).
func SetRoutePointObserver(f func(where string)) { routePointObserver.Store(&f) }

// noteRoutePointRefused counts a refusal and logs a WARN with where and the drive's external id only, never a coordinate (driveID "" logs
// nothing: the encoder does not know which drive it is encoding).
func noteRoutePointRefused(where, driveID string) {
	if f := routePointObserver.Load(); f != nil && *f != nil {
		(*f)(where)
	}
	if driveID != "" {
		slog.Warn("route point refused", "where", where, "drive_id", driveID)
	}
}

// encodePolylineSkipped is encodePolyline that also says how many points it left out (the recorder logs a WARN with the drive's id).
func encodePolylineSkipped(points [][2]float64) (string, int) {
	n := 0
	for _, p := range points {
		if !validCoord(p[0], p[1]) {
			n++
		}
	}
	return encodePolyline(points), n
}
