package api

import "math"

// elevationProfileMaxPoints bounds the series on the wire (about 3 KB).
const elevationProfileMaxPoints = 200

// elevationProfileDTO is the terrain height along the plan's first route, for the chart under the map. Points are [distance_m, elevation_m]
// pairs in whole metres (SI whatever the account's units; clients convert), WaypointAtM has one entry per routes[0].waypoints (null: that stop
// could not be placed on the line). The whole field is absent when any leg could not be read: no partial series, no zeros.
type elevationProfileDTO struct {
	Points      [][2]int `json:"points"`
	WaypointAtM []*int   `json:"waypoint_at_m"`
}

// profile joins the legs' raw heights into one series. It is nil unless every leg was read and at least two points result.
func (rc routeClimbResult) profile() *elevationProfileDTO {
	if len(rc.Legs) == 0 || len(rc.StopAtM) != len(rc.Legs)+1 {
		return nil
	}
	var d, h []float64
	for i, l := range rc.Legs {
		if !l.OK {
			return nil
		}
		from := 0
		if i > 0 {
			from = 1 // the join point is the previous leg's last sample
		}
		d, h = append(d, l.DistM[from:]...), append(h, l.HeightsM[from:]...)
	}
	pts := downsampleProfile(d, h, elevationProfileMaxPoints)
	if len(pts) < 2 {
		return nil
	}
	at := make([]*int, len(rc.StopAtM))
	for i, m := range rc.StopAtM {
		if m != nil {
			v := int(math.Round(*m))
			at[i] = &v
		}
	}
	return &elevationProfileDTO{Points: pts, WaypointAtM: at}
}

// downsampleProfile reduces the series to at most max points. The first and last samples are kept exactly. Each of the max-2 distance bins
// between keeps ONE real sample: the global highest and lowest samples where they fall, else the one furthest from the chord of its bin, so a
// summit or a valley is never averaged away and the chart's extremes are the series' extremes.
func downsampleProfile(d, h []float64, max int) [][2]int {
	n := len(d)
	if n == 0 || n != len(h) {
		return nil
	}
	pt := func(i int) [2]int { return [2]int{int(math.Round(d[i])), int(math.Round(h[i]))} }
	if n <= max {
		out := make([][2]int, n)
		for i := range out {
			out[i] = pt(i)
		}
		return out
	}
	hi, lo := 1, 1
	for i := 1; i < n-1; i++ {
		if h[i] > h[hi] {
			hi = i
		}
		if h[i] < h[lo] {
			lo = i
		}
	}
	bins := max - 2
	span := d[n-1] - d[0]
	binOf := func(i int) int {
		if span <= 0 {
			return (i - 1) * bins / (n - 2)
		}
		b := int((d[i] - d[0]) / span * float64(bins))
		if b >= bins {
			b = bins - 1
		}
		return b
	}
	out := make([][2]int, 0, max)
	out = append(out, pt(0))
	i := 1
	for i < n-1 {
		b := binOf(i)
		j := i
		for j < n-1 && binOf(j) == b {
			j++
		}
		// Samples i..j-1 are this bin.
		pick := -1
		switch {
		case i <= hi && hi < j && i <= lo && lo < j:
			pick = hi
			if h[lo] < h[hi] && math.Abs(h[lo]-chord(d, h, i, j-1, lo)) > math.Abs(h[hi]-chord(d, h, i, j-1, hi)) {
				pick = lo
			}
		case i <= hi && hi < j:
			pick = hi
		case i <= lo && lo < j:
			pick = lo
		default:
			best := -1.0
			for k := i; k < j; k++ {
				if dev := math.Abs(h[k] - chord(d, h, i, j-1, k)); dev > best {
					best, pick = dev, k
				}
			}
		}
		out = append(out, pt(pick))
		i = j
	}
	return append(out, pt(n-1))
}

// chord is the height at sample k on the straight line from sample a to sample b.
func chord(d, h []float64, a, b, k int) float64 {
	if d[b] == d[a] {
		return h[a]
	}
	return h[a] + (h[b]-h[a])*(d[k]-d[a])/(d[b]-d[a])
}
