package chargers

import "math"

// tileSlackM is added to the corridor when a tile is tested by its centre: half a z14 tile's diagonal is about 1.8 km at the
// equator (less northward); 2.5 km covers it with room.
const tileSlackM = 2500.0

// pathIndexChunk is how many segments one bounding box covers.
const pathIndexChunk = 32

// pathIndex speeds up "is this point within limit metres of the path?" for a path of thousands of segments: the segments are
// grouped in chunks with a bounding box each, and a point only walks the segments of chunks whose box, grown by limit, contains
// it. Exact: a point farther than limit from every segment is in no grown box, and one within limit of a segment is in the
// grown box of that segment's chunk.
type pathIndex struct {
	minX, maxX, minY, maxY []float64
}

func newPathIndex(pp projectedPath) pathIndex {
	n := len(pp.xs)
	var ix pathIndex
	for i := 0; i+1 < n; i += pathIndexChunk {
		end := i + pathIndexChunk
		if end > n-1 {
			end = n - 1
		}
		minX, maxX := math.Inf(1), math.Inf(-1)
		minY, maxY := math.Inf(1), math.Inf(-1)
		for j := i; j <= end; j++ {
			minX, maxX = math.Min(minX, pp.xs[j]), math.Max(maxX, pp.xs[j])
			minY, maxY = math.Min(minY, pp.ys[j]), math.Max(maxY, pp.ys[j])
		}
		ix.minX, ix.maxX = append(ix.minX, minX), append(ix.maxX, maxX)
		ix.minY, ix.maxY = append(ix.minY, minY), append(ix.maxY, maxY)
	}
	return ix
}

// near reports whether (lat, lon) is within limit metres of some segment of the path, in the path's flat projection.
func (ix pathIndex) near(pp projectedPath, lat, lon, limit float64) bool {
	px := lon * pp.cosRef * (earthRadiusM * math.Pi / 180)
	py := lat * (earthRadiusM * math.Pi / 180)
	limit2 := limit * limit
	for c := range ix.minX {
		if px < ix.minX[c]-limit || px > ix.maxX[c]+limit || py < ix.minY[c]-limit || py > ix.maxY[c]+limit {
			continue
		}
		lo := c * pathIndexChunk
		hi := lo + pathIndexChunk
		if hi > len(pp.xs)-1 {
			hi = len(pp.xs) - 1
		}
		for i := lo + 1; i <= hi; i++ {
			ax, ay := pp.xs[i-1], pp.ys[i-1]
			dx, dy := pp.xs[i]-ax, pp.ys[i]-ay
			seg2 := dx*dx + dy*dy
			t := 0.0
			if seg2 > 0 {
				t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/seg2))
			}
			ex, ey := px-(ax+t*dx), py-(ay+t*dy)
			if ex*ex+ey*ey <= limit2 {
				return true
			}
		}
	}
	return false
}

// tileCenter is the latitude and longitude of the centre of tile (x, y) at zoom z.
func tileCenter(x, y uint32, z uint8) (lat, lon float64) {
	n := float64(int(1) << z)
	lon = (float64(x)+0.5)/n*360 - 180
	lat = math.Atan(math.Sinh(math.Pi*(1-2*(float64(y)+0.5)/n))) * 180 / math.Pi
	return lat, lon
}
