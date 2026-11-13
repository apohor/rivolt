package tripshare

import (
	"math"

	"github.com/apohor/rivolt/internal/maps"
)

// A route line is shared with its ends cut away so it cannot be walked back to an address (DA's S1 and S3). The cut is by straight-line
// distance, not distance along the route: every point within a radius of a circle's centre is dropped, wherever it lies on the line, so a
// loop that comes back into the circle is cut again and the road graph cannot be walked back from the first kept point.

// Circle is a private place: the start, the destination, a via, an overnight, the saved home.
type Circle struct {
	Lat, Lon, RadiusM float64
}

const (
	earthR = 6371000.0
	// subdivideStepM is the spacing of the points tested on an edge that comes near a circle: an edge between two vertices outside a
	// circle can still cross it.
	subdivideStepM = 100.0
	// nearMarginM widens "comes near" so a tangent edge is subdivided too.
	nearMarginM = 200.0
)

func rad(d float64) float64 { return d * math.Pi / 180 }

// DistM is the great-circle distance in metres.
func DistM(aLat, aLon, bLat, bLon float64) float64 {
	dLat, dLon := rad(bLat-aLat), rad(bLon-aLon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(aLat))*math.Cos(rad(bLat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthR * math.Asin(math.Sqrt(h))
}

// segDistM is the distance from point p to the segment a-b in a local flat projection around p (accurate at the scale of a road edge).
func segDistM(pLat, pLon, aLat, aLon, bLat, bLon float64) float64 {
	k := math.Cos(rad(pLat))
	ax, ay := rad(aLon-pLon)*k*earthR, rad(aLat-pLat)*earthR
	bx, by := rad(bLon-pLon)*k*earthR, rad(bLat-pLat)*earthR
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
	}
	return math.Hypot(ax+t*dx, ay+t*dy)
}

func inAny(cs []Circle, lat, lon float64) bool {
	for _, c := range cs {
		if DistM(c.Lat, c.Lon, lat, lon) < c.RadiusM {
			return true
		}
	}
	return false
}

func edgeNear(cs []Circle, aLat, aLon, bLat, bLon float64) bool {
	for _, c := range cs {
		if segDistM(c.Lat, c.Lon, aLat, aLon, bLat, bLon) < c.RadiusM+nearMarginM {
			return true
		}
	}
	return false
}

// point is a vertex of the line with where it sits along the ORIGINAL route.
type point struct {
	lat, lon float64
	cum      float64 // metres from the start of the original line
	keep     bool
}

// Segment is a run of the line outside every circle.
type Segment struct {
	Points [][2]float64 // lat, lon
	// FromM and ToM are where the run starts and ends along the original line.
	FromM, ToM float64
	// CollapsedFromM is where the run starts on the shared axis, which closes the gaps (so the gap lengths, which would give the cut radius
	// away, are not on the wire).
	CollapsedFromM float64
}

// Cut returns the runs of the line that lie outside every circle, in order. Runs of fewer than two points are dropped. The total length of
// the original line is returned too.
func Cut(line [][2]float64, cs []Circle) ([]Segment, float64) {
	if len(line) < 2 {
		return nil, 0
	}
	var pts []point
	cum := 0.0
	add := func(lat, lon, c float64) {
		pts = append(pts, point{lat: lat, lon: lon, cum: c, keep: !inAny(cs, lat, lon)})
	}
	add(line[0][0], line[0][1], 0)
	for i := 1; i < len(line); i++ {
		a, b := line[i-1], line[i]
		l := DistM(a[0], a[1], b[0], b[1])
		if l > subdivideStepM && edgeNear(cs, a[0], a[1], b[0], b[1]) {
			n := int(math.Ceil(l / subdivideStepM))
			for s := 1; s < n; s++ {
				f := float64(s) / float64(n)
				add(a[0]+(b[0]-a[0])*f, a[1]+(b[1]-a[1])*f, cum+l*f)
			}
		}
		cum += l
		add(b[0], b[1], cum)
	}
	var out []Segment
	collapsed := 0.0
	flush := func(run []point) {
		if len(run) < 2 {
			return
		}
		s := Segment{FromM: run[0].cum, ToM: run[len(run)-1].cum, CollapsedFromM: collapsed}
		for _, p := range run {
			s.Points = append(s.Points, [2]float64{p.lat, p.lon})
		}
		collapsed += s.ToM - s.FromM
		out = append(out, s)
	}
	var run []point
	for _, p := range pts {
		if p.keep {
			run = append(run, p)
			continue
		}
		flush(run)
		run = nil
	}
	flush(run)
	return out, cum
}

// Encode is the segment as a polyline6 string.
func (s Segment) Encode() string { return maps.EncodePolyline6(s.Points) }

// Collapse maps a distance along the original line to the shared axis, or false when it falls in a cut.
func Collapse(segs []Segment, m float64) (float64, bool) {
	for _, s := range segs {
		if m >= s.FromM && m <= s.ToM {
			return s.CollapsedFromM + (m - s.FromM), true
		}
	}
	return 0, false
}

// NearestKept is the distance along the shared axis of the kept point nearest to (lat, lon), and false when none is within maxM.
func NearestKept(segs []Segment, lat, lon, maxM float64) (float64, bool) {
	best, at := maxM, -1.0
	for _, s := range segs {
		for i, p := range s.Points {
			d := DistM(lat, lon, p[0], p[1])
			if d <= best {
				// Distance along the segment is not stored per point, so measure it from the segment start.
				best, at = d, s.CollapsedFromM+alongM(s.Points, i)
			}
		}
	}
	return at, at >= 0
}

func alongM(pts [][2]float64, i int) float64 {
	d := 0.0
	for j := 1; j <= i; j++ {
		d += DistM(pts[j-1][0], pts[j-1][1], pts[j][0], pts[j][1])
	}
	return d
}
