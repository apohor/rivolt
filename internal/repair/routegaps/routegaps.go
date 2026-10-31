// Package routegaps fills the straight lines in recorded drive routes.
//
// While a drive is recorded, the recorder asks Valhalla for a road-snapped shape whenever two consecutive GPS fixes are more than
// 10 s and 100 m apart (rivian.appendDrivePath), so a dropped-fix stretch is drawn along the road. A monitor the boot hydrate sweep
// started never had the route filler installed (fixed in #1356), so its drives show a straight segment across each dropout. This
// package finds those segments in the stored polyline (consecutive vertices far apart) and splices in the same shape the recorder
// would have. Only drives.route_polyline changes; no distance, energy or other figure does.
package routegaps

import (
	"context"
	"errors"
	"math"
	"time"
)

const (
	// DefaultMinGapM is how far apart two consecutive vertices must be to count as a dropout. The recorder appends a vertex per
	// distinct fix (a few seconds apart, under 150 m at highway speed), so a gap this wide is a dropout, not a straight road.
	DefaultMinGapM = 500.0
	// MaxGapM matches the recorder: a longer jump is not a dropout (a teleport, a ferry, a bad fix) and is left alone.
	MaxGapM = 50_000.0
	// MaxFillsPerDrive bounds the Valhalla calls one drive may cost.
	MaxFillsPerDrive = 12
	// CallTimeout bounds one Valhalla call.
	CallTimeout = 3 * time.Second
)

// Filler returns the on-road shape between two points, endpoints included. maps.Valhalla implements it.
type Filler interface {
	RouteShape(ctx context.Context, from, to [2]float64) ([][2]float64, error)
}

// Decode reads a Google polyline at precision 5, the format drives.route_polyline stores.
func Decode(s string) ([][2]float64, error) {
	var out [][2]float64
	var lat, lon int
	for i := 0; i < len(s); {
		var d [2]int
		for k := 0; k < 2; k++ {
			shift, result := 0, 0
			for {
				if i >= len(s) {
					return nil, errors.New("routegaps: truncated polyline")
				}
				b := int(s[i]) - 63
				i++
				if b < 0 || b > 63 {
					return nil, errors.New("routegaps: bad polyline character")
				}
				result |= (b & 0x1f) << shift
				shift += 5
				if b < 0x20 {
					break
				}
				if shift > 30 {
					return nil, errors.New("routegaps: polyline number too long")
				}
			}
			if result&1 != 0 {
				d[k] = ^(result >> 1)
			} else {
				d[k] = result >> 1
			}
		}
		lat += d[0]
		lon += d[1]
		out = append(out, [2]float64{float64(lat) / 1e5, float64(lon) / 1e5})
	}
	return out, nil
}

func haversineM(a, b [2]float64) float64 {
	const r = 6371000.0
	toRad := math.Pi / 180
	dLat := (b[0] - a[0]) * toRad
	dLon := (b[1] - a[1]) * toRad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(a[0]*toRad)*math.Cos(b[0]*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}

// Gaps returns the index i of every segment points[i]..points[i+1] that is a dropout: longer than minGapM and shorter than MaxGapM.
func Gaps(points [][2]float64, minGapM float64) []int {
	var out []int
	for i := 0; i+1 < len(points); i++ {
		if d := haversineM(points[i], points[i+1]); d > minGapM && d < MaxGapM {
			out = append(out, i)
		}
	}
	return out
}

// Result is what Fill did to one drive's points.
type Result struct {
	Points [][2]float64
	Gaps   int // dropouts found
	Filled int // spliced
	Failed int // Valhalla refused, timed out or returned no shape; or beyond MaxFillsPerDrive
}

// Fill splices the road shape into every dropout (up to MaxFillsPerDrive), as the recorder does: the shape's first and last vertex
// are the two fixes already in the path and are dropped. A dropout Valhalla cannot fill stays a straight line.
func Fill(ctx context.Context, points [][2]float64, f Filler, minGapM float64) Result {
	gaps := Gaps(points, minGapM)
	res := Result{Gaps: len(gaps)}
	if len(gaps) == 0 {
		res.Points = points
		return res
	}
	isGap := make(map[int]bool, len(gaps))
	for _, g := range gaps {
		isGap[g] = true
	}
	tried := 0
	out := make([][2]float64, 0, len(points)+64)
	for i, p := range points {
		out = append(out, p)
		if !isGap[i] {
			continue
		}
		if tried >= MaxFillsPerDrive {
			res.Failed++
			continue
		}
		tried++
		cctx, cancel := context.WithTimeout(ctx, CallTimeout)
		shape, err := f.RouteShape(cctx, p, points[i+1])
		cancel()
		if err != nil || len(shape) <= 2 {
			res.Failed++
			continue
		}
		out = append(out, shape[1:len(shape)-1]...)
		res.Filled++
	}
	res.Points = out
	return res
}

// Month is "2006-01" for a drive's start, for the dry run's counts.
func Month(t time.Time) string { return t.UTC().Format("2006-01") }
