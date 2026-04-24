import type { Drive } from "./api";

// collapseRoundTrips merges consecutive drive pairs that form an
// out-and-back round trip into a single row: two drives where the
// second ends within `radiusMeters` of the first's start point, and
// the park-gap between them is at most `maxGapMinutes`. Motivation:
// drive to the gym, park for 20 minutes, drive home — today this
// records as two separate drives. With the merge the list shows a
// single trip starting and ending at home.
//
// Pure: the input (assumed DESC by StartedAt, i.e. the ListRecent
// contract) is not mutated. Only pairs are folded; a 3-stop chain
// (A→B→C→A) won't fully collapse in one pass — good enough for the
// dashboard and avoids surprising rewrites.
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
    const cur = asc[i];
    const nxt = asc[i + 1];
    if (nxt && isRoundTrip(cur, nxt, radiusMeters, maxGapMinutes)) {
      merged.push(mergePair(cur, nxt));
      i += 2;
    } else {
      merged.push(cur);
      i += 1;
    }
  }
  return merged.reverse();
}

function isRoundTrip(
  a: Drive,
  b: Drive,
  radiusMeters: number,
  maxGapMinutes: number,
): boolean {
  if (a.VehicleID !== b.VehicleID) return false;
  const gapMs = new Date(b.StartedAt).getTime() - new Date(a.EndedAt).getTime();
  if (gapMs < 0 || gapMs > maxGapMinutes * 60_000) return false;
  const d = haversineMeters(a.StartLat, a.StartLon, b.EndLat, b.EndLon);
  return d <= radiusMeters;
}

function mergePair(a: Drive, b: Drive): Drive {
  const d1 =
    (new Date(a.EndedAt).getTime() - new Date(a.StartedAt).getTime()) / 1000;
  const d2 =
    (new Date(b.EndedAt).getTime() - new Date(b.StartedAt).getTime()) / 1000;
  const total = d1 + d2;
  const avg =
    total > 0
      ? (a.AvgSpeedMph * d1 + b.AvgSpeedMph * d2) / total
      : a.AvgSpeedMph;
  return {
    ...a,
    EndedAt: b.EndedAt,
    EndSoCPct: b.EndSoCPct,
    EndOdometerMi: b.EndOdometerMi,
    EndLat: b.EndLat,
    EndLon: b.EndLon,
    DistanceMi: a.DistanceMi + b.DistanceMi,
    MaxSpeedMph: Math.max(a.MaxSpeedMph, b.MaxSpeedMph),
    AvgSpeedMph: avg,
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
