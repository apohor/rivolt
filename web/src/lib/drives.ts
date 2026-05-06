import type { Drive } from "./api";

// collapseRoundTrips merges chains of consecutive drives that form a
// closed loop into a single row. A chain [D1 … Dn] qualifies when:
//   - every consecutive gap (Di.EndedAt → Di+1.StartedAt) ≤ maxGapMinutes, and
//   - the chain's last drive ends within radiusMeters of the first
//     drive's start point.
//
// The greedy algorithm scans ascending and, at each position i, extends
// the chain as far as the gap constraint allows, then picks the farthest
// j for which the endpoint lands within radius of D[i].Start. This
// handles 2-leg round trips (gym-and-back), 3-leg chains (A→B→C→A), and
// arbitrary multi-stop trips in a single O(n²) pass.
//
// Pure: the input (assumed DESC by StartedAt, i.e. the ListRecent
// contract) is not mutated.
export function collapseRoundTrips(
  ds: Drive[],
  radiusMeters: number,
  maxGapMinutes: number,
): Drive[] {
  if (ds.length < 2) return ds.slice();
  // ListRecent returns DESC; walk ascending so previous/next has
  // their natural chronological meaning.
  const asc = ds.slice().reverse();
  const merged: Drive[] = [];
  let i = 0;
  while (i < asc.length) {
    // Find the farthest j whose chain endpoint lands within radius of
    // asc[i].Start, while all consecutive gaps stay within maxGap.
    let best = i;
    for (let j = i + 1; j < asc.length; j++) {
      const gap =
        new Date(asc[j].StartedAt).getTime() -
        new Date(asc[j - 1].EndedAt).getTime();
      if (gap < 0 || gap > maxGapMinutes * 60_000) break;
      if (
        haversineMeters(
          asc[i].StartLat,
          asc[i].StartLon,
          asc[j].EndLat,
          asc[j].EndLon,
        ) <= radiusMeters
      ) {
        best = j;
      }
    }
    merged.push(best > i ? mergeChain(asc.slice(i, best + 1)) : asc[i]);
    i = best + 1;
  }
  return merged.reverse();
}

// mergeChain folds an ordered slice of drives into one aggregate row.
// Energy rule: only sum energy when every leg with meaningful distance
// (> 0.1 mi) carries a non-zero EnergyUsedKWh. Phantom stubs are
// excluded from this gate so they don't zero out an otherwise complete
// merged row.
function mergeChain(chain: Drive[]): Drive {
  if (chain.length === 1) return chain[0];
  const last = chain[chain.length - 1];

  let totalDist = 0,
    maxSpd = 0,
    wSpd = 0,
    wDur = 0,
    totalEnergy = 0;
  let allHaveEnergy = true;
  let totalCost = 0;
  let hasCost = true;

  for (const d of chain) {
    totalDist += d.DistanceMi;
    if (d.MaxSpeedMph > maxSpd) maxSpd = d.MaxSpeedMph;
    const dur =
      (new Date(d.EndedAt).getTime() - new Date(d.StartedAt).getTime()) / 1000;
    if (dur > 0) {
      wSpd += d.AvgSpeedMph * dur;
      wDur += dur;
    }
    if (d.DistanceMi > 0.1) {
      if (d.EnergyUsedKWh > 0) totalEnergy += d.EnergyUsedKWh;
      else allHaveEnergy = false;
    }
    if (d.estimated_cost != null) totalCost += d.estimated_cost;
    else hasCost = false;
  }

  const energy = allHaveEnergy ? totalEnergy : 0;
  const cost = hasCost ? totalCost : undefined;
  const cur = chain[0].estimated_currency || last.estimated_currency;
  const rate = cost != null && energy > 0 ? cost / energy : undefined;

  return {
    ...chain[0],
    EndedAt: last.EndedAt,
    EndSoCPct: last.EndSoCPct,
    EndOdometerMi: last.EndOdometerMi,
    EndLat: last.EndLat,
    EndLon: last.EndLon,
    DistanceMi: totalDist,
    MaxSpeedMph: maxSpd,
    AvgSpeedMph: wDur > 0 ? wSpd / wDur : chain[0].AvgSpeedMph,
    EnergyUsedKWh: energy,
    estimated_cost: cost,
    estimated_currency: cur,
    estimated_price_per_kwh: rate,
  };
}

// Great-circle distance in meters on a spherical earth. Missing
// coords (0,0 sentinel) return +Infinity so pairs with unknown
// location never merge.
function haversineMeters(
  lat1: number,
  lon1: number,
  lat2: number,
  lon2: number,
): number {
  if ((lat1 === 0 && lon1 === 0) || (lat2 === 0 && lon2 === 0)) {
    return Number.POSITIVE_INFINITY;
  }
  const R = 6_371_000;
  const toRad = (d: number) => (d * Math.PI) / 180;
  const dLat = toRad(lat2 - lat1);
  const dLon = toRad(lon2 - lon1);
  const a =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2;
  const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
  return R * c;
}
