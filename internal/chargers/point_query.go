package chargers

import "math"

// PointQueryPath is the path to give QueryCorridor to ask for everything within radiusKm of a point (use CorridorKm = radiusKm, and
// filter the answer by distance if the exact circle matters). QueryCorridor is a band along a path and drops a station whose nearest
// point on the path is an end of it, so a tiny path around a point, or the point twice, finds nothing at all: only a station in a
// thin strip beside it would pass. A north-south segment through the point, longer than the radius on each side, keeps every station
// within the radius inside the band.
func PointQueryPath(lat, lon, radiusKm float64) [][2]float64 {
	d := radiusKm * 1.25 / 111.195 // degrees of latitude; the segment runs a quarter longer than the radius each way
	lo, hi := math.Max(-89.9, lat-d), math.Min(89.9, lat+d)
	return [][2]float64{{lo, lon}, {hi, lon}}
}
