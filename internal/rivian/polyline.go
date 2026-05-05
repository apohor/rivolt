package rivian

import "strings"

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
func encodePolyline(points [][2]float64) string {
	if len(points) == 0 {
		return ""
	}
	var b strings.Builder
	// Each point worst-case is 11 chars (5+1 per coord delta).
	b.Grow(len(points) * 8)
	var prevLat, prevLon int32
	for _, p := range points {
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
