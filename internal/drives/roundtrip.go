package drives

import (
	"math"
	"time"
)

// CollapseRoundTrips merges chains of consecutive drives that form a
// closed loop into a single row. A chain [D1 … Dn] qualifies when:
//   - every consecutive gap (Di.EndedAt → Di+1.StartedAt) ≤ maxGap, and
//   - the chain's last drive ends within radiusMeters of the first
//     drive's start point.
//
// The greedy algorithm scans ascending and, at each position i, extends
// the chain as far as the gap constraint allows, then picks the farthest
// j for which the endpoint lands within radius of D[i].Start. This
// handles 2-leg round trips (gym-and-back), 3-leg chains (A→B→C→A), and
// arbitrary multi-stop trips in a single O(n²) pass — no iteration
// needed. The input is expected in DESC order (ListRecent contract) and
// the output preserves that order.
//
// Pure function — DB rows are never mutated, so raw data is preserved
// and the pairing rule can be tuned / replayed at will.
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
		// Extend the candidate chain as far as the gap constraint
		// allows, recording the farthest j whose endpoint lands
		// within radius of asc[i].Start.
		best := i
		for j := i + 1; j < len(asc); j++ {
			gap := asc[j].StartedAt.Sub(asc[j-1].EndedAt)
			if gap < 0 || gap > maxGap {
				break
			}
			if haversineMeters(asc[i].StartLat, asc[i].StartLon,
				asc[j].EndLat, asc[j].EndLon) <= radiusMeters {
				best = j
			}
		}
		if best > i {
			merged = append(merged, mergeChain(asc[i:best+1]))
		} else {
			merged = append(merged, asc[i])
		}
		i = best + 1
	}

	// Re-descend to match ListRecent's contract.
	out := make([]Drive, len(merged))
	for i, d := range merged {
		out[len(merged)-1-i] = d
	}
	return out
}

// mergeChain folds an ordered slice of drives into one aggregate row.
// The result inherits the first drive's ID and start fields; the last
// drive's end fields close it out. Distance, energy, and speed are
// accumulated across all legs.
//
// Energy rule: only sum pack-side energy when every leg with meaningful
// distance (> 0.01 mi) carries a non-zero EnergyUsedKWh. Phantom stubs
// (sub-second gear bounces with ~0 distance) are excluded from this
// gate so they don't zero out an otherwise complete merged row.
func mergeChain(chain []Drive) Drive {
	if len(chain) == 1 {
		return chain[0]
	}
	last := chain[len(chain)-1]
	m := chain[0]
	m.EndedAt = last.EndedAt
	m.EndSoCPct = last.EndSoCPct
	m.EndOdometerMi = last.EndOdometerMi
	m.EndLat = last.EndLat
	m.EndLon = last.EndLon

	var totalDist, maxSpd float64
	var wSpd, wDur float64
	var totalEnergy float64
	allHaveEnergy := true

	for _, d := range chain {
		totalDist += d.DistanceMi
		if d.MaxSpeedMph > maxSpd {
			maxSpd = d.MaxSpeedMph
		}
		dur := d.EndedAt.Sub(d.StartedAt).Seconds()
		if dur > 0 {
			wSpd += d.AvgSpeedMph * dur
			wDur += dur
		}
		// Energy gate. Two leg classes legitimately carry zero
		// EnergyUsedKWh and must NOT gate the merged total:
		//
		//   1. Phantom stubs — sub-second gear bounces with ~0 mi.
		//      Excluded by the DistanceMi > 0.01 guard.
		//
		//   2. SoC-quantization-limited legs — short legs whose
		//      consumption was below the ~1% SoC resolution Rivian
		//      reports, so start_soc == end_soc and the recorder's
		//      SoC-delta energy estimator correctly returns 0. A
		//      0.3 mi errand around the corner is the canonical
		//      example: ~0.1 kWh used, well below the ~1.5 kWh
		//      needed to tick a percent on a R1S pack. Without
		//      this carve-out a single such sub-leg silently
		//      zeros the entire round trip's energy / cost /
		//      efficiency.
		//
		// We only gate when SoC actually dropped but EnergyUsedKWh
		// is still zero — that's the "real consumption, missing
		// estimate" case where reporting an aggregate would be a
		// lie.
		if d.DistanceMi > 0.01 {
			socDropped := d.StartSoCPct > d.EndSoCPct
			switch {
			case d.EnergyUsedKWh > 0:
				totalEnergy += d.EnergyUsedKWh
			case socDropped:
				allHaveEnergy = false
			}
		}
	}

	m.DistanceMi = totalDist
	m.MaxSpeedMph = maxSpd
	if wDur > 0 {
		m.AvgSpeedMph = wSpd / wDur
	}
	if allHaveEnergy {
		m.EnergyUsedKWh = totalEnergy
	} else {
		m.EnergyUsedKWh = 0
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
