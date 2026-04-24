package drives

import (
	"math"
	"time"
)

// CollapseRoundTrips merges consecutive drive pairs that form an
// out-and-back round trip into a single row: two drives where the
// second ends within radiusMeters of the first's start point, and
// the park-gap between them is under maxGap. Motivating case: drive
// to the gym, park for 20 minutes, drive home — today that records
// as two separate drives. With this pass the dashboard shows a
// single logical trip that starts and ends at home.
//
// Operates on the slice returned by Store.ListRecent (descending by
// StartedAt) and returns a new slice in the same order, with pairs
// collapsed. Pure function — DB rows are never mutated, so raw data
// is preserved and the pairing rule can be tuned / replayed at will.
//
// Scope: pairs only. 3-stop chains (A→B→C→A) aren't folded in one
// pass; if the dataset warrants it we can iterate until fixed point.
func CollapseRoundTrips(ds []Drive, radiusMeters float64, maxGap time.Duration) []Drive {
	if len(ds) < 2 {
		out := make([]Drive, len(ds))
		copy(out, ds)
		return out
	}
	// ListRecent returns DESC; work left-to-right on the ascending copy
	// so "previous" / "next" has its natural chronological meaning.
	asc := make([]Drive, len(ds))
	for i, d := range ds {
		asc[len(ds)-1-i] = d
	}
	merged := make([]Drive, 0, len(asc))
	i := 0
	for i < len(asc) {
		cur := asc[i]
		if i+1 < len(asc) {
			nxt := asc[i+1]
			gap := nxt.StartedAt.Sub(cur.EndedAt)
			if cur.VehicleID == nxt.VehicleID &&
				gap >= 0 && gap <= maxGap &&
				haversineMeters(cur.StartLat, cur.StartLon, nxt.EndLat, nxt.EndLon) <= radiusMeters {
				merged = append(merged, mergePair(cur, nxt))
				i += 2
				continue
			}
		}
		merged = append(merged, cur)
		i++
	}
	// Re-descend to match ListRecent's contract.
	out := make([]Drive, len(merged))
	for i, d := range merged {
		out[len(merged)-1-i] = d
	}
	return out
}

func mergePair(a, b Drive) Drive {
	m := a
	m.EndedAt = b.EndedAt
	m.EndSoCPct = b.EndSoCPct
	m.EndOdometerMi = b.EndOdometerMi
	m.EndLat = b.EndLat
	m.EndLon = b.EndLon
	m.DistanceMi = a.DistanceMi + b.DistanceMi
	if b.MaxSpeedMph > m.MaxSpeedMph {
		m.MaxSpeedMph = b.MaxSpeedMph
	}
	// Duration-weighted avg: a 3-minute crawl shouldn't drag the mean
	// as much as an hour of steady-state cruising.
	d1 := a.EndedAt.Sub(a.StartedAt).Seconds()
	d2 := b.EndedAt.Sub(b.StartedAt).Seconds()
	if total := d1 + d2; total > 0 {
		m.AvgSpeedMph = (a.AvgSpeedMph*d1 + b.AvgSpeedMph*d2) / total
	}
	return m
}

// haversineMeters is the great-circle distance between two lat/lon
// points on a spherical earth. Missing coords (0,0 sentinel) return
// +Inf so pairs with unknown location never get merged.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	if (lat1 == 0 && lon1 == 0) || (lat2 == 0 && lon2 == 0) {
		return math.Inf(1)
	}
	const r = 6371000.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLon := rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}
