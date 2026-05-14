// GPS trace pre-processing for map-matching.
//
// HMM-based matchers (OSRM, Valhalla) are sensitive to a handful of
// noise patterns that arise in raw Rivian telemetry:
//   - identical points repeated while the vehicle sits at a light;
//   - GPS jumps (single sample tens or hundreds of meters off the
//     actual path) that imply impossible speeds;
//   - high-frequency samples crammed within a single GPS-accuracy
//     radius — useful for the speed chart, useless for the matcher.
// Cleaning the trace up-front gives the matcher a sequence of points
// that actually trace a road, which is most of what a high-quality
// snap costs in practice.

export type TracePoint = { lat: number; lon: number; t?: number };

const MAX_SANE_SPEED_MPS = 45; // ~100 mph implied between adjacent points
const GAP_TIME_S = 120; // ≥2 min between samples = end-of-segment
const GAP_DIST_M = 500; // ≥500 m jump between samples = end-of-segment

// haversine distance in meters.
function haversine(a: TracePoint, b: TracePoint): number {
  const R = 6371000;
  const φ1 = (a.lat * Math.PI) / 180;
  const φ2 = (b.lat * Math.PI) / 180;
  const dφ = ((b.lat - a.lat) * Math.PI) / 180;
  const dλ = ((b.lon - a.lon) * Math.PI) / 180;
  const x =
    Math.sin(dφ / 2) ** 2 +
    Math.cos(φ1) * Math.cos(φ2) * Math.sin(dλ / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(x));
}

// cleanTrace runs four cheap passes over the input samples and
// returns the result in the same SnapPoint-compatible shape:
//   1. drop unusable (missing lat/lon),
//   2. dedup consecutive identical coords,
//   3. reject single-sample GPS jumps that imply >MAX_SANE_SPEED,
//   4. collapse long runs of stopped samples to a single anchor.
export function cleanTrace<T extends TracePoint>(pts: T[]): T[] {
  if (pts.length === 0) return pts;
  // 1. drop unusable
  let s: T[] = pts.filter(
    (p) =>
      Number.isFinite(p.lat) &&
      Number.isFinite(p.lon) &&
      Math.abs(p.lat) <= 90 &&
      Math.abs(p.lon) <= 180,
  );
  if (s.length === 0) return s;

  // 2. dedup consecutive same coord
  s = s.filter(
    (p, i) => i === 0 || p.lat !== s[i - 1].lat || p.lon !== s[i - 1].lon,
  );

  // 3. drop single-sample outliers: if the implied speed both into and
  // out of a sample exceeds the sane ceiling, the middle sample is
  // the bad one. Keep endpoints (no neighbors to compare).
  if (s.length >= 3) {
    const kept: T[] = [s[0]];
    for (let i = 1; i < s.length - 1; i++) {
      const prev = kept[kept.length - 1];
      const cur = s[i];
      const next = s[i + 1];
      const dtIn =
        cur.t != null && prev.t != null ? Math.max(0.5, cur.t - prev.t) : 1;
      const dtOut =
        next.t != null && cur.t != null ? Math.max(0.5, next.t - cur.t) : 1;
      const vIn = haversine(prev, cur) / dtIn;
      const vOut = haversine(cur, next) / dtOut;
      if (vIn > MAX_SANE_SPEED_MPS && vOut > MAX_SANE_SPEED_MPS) {
        continue;
      }
      kept.push(cur);
    }
    kept.push(s[s.length - 1]);
    s = kept;
  }

  // 4. collapse stop runs. A "stop run" is ≥3 consecutive samples
  // whose pairwise distance is sub-meter; we drop the middle samples
  // and keep just the run's endpoints so the matcher sees a single
  // pause rather than a wall of identical readings.
  if (s.length >= 3) {
    const kept: T[] = [];
    let runStart = -1;
    for (let i = 0; i < s.length; i++) {
      const cur = s[i];
      const prev = i > 0 ? s[i - 1] : null;
      const stopped =
        prev !== null &&
        haversine(prev, cur) < 1 &&
        (cur.t == null || prev.t == null || cur.t - prev.t < 60);
      if (stopped) {
        if (runStart < 0) runStart = i - 1;
        continue;
      }
      if (runStart >= 0) {
        // Emit the last sample of the previous stop run as the anchor.
        const anchor = s[i - 1];
        if (
          kept.length === 0 ||
          kept[kept.length - 1] !== anchor
        ) {
          // Already kept runStart's sample as the run's first anchor
          // when we entered the run on iter i = runStart+1; we just
          // need the closing one if it differs from what we kept.
          kept.push(anchor);
        }
        runStart = -1;
      }
      kept.push(cur);
    }
    // If the trace ended mid-run, close it out.
    if (runStart >= 0) {
      kept.push(s[s.length - 1]);
    }
    s = kept;
  }
  return s;
}

// splitTraceOnGaps cuts the cleaned trace wherever consecutive
// samples are separated by ≥ GAP_TIME_S seconds OR ≥ GAP_DIST_M
// meters. Each cut returns the two endpoint coords so the renderer
// can draw a "missing data" connector — the car drove between
// them, telemetry just didn't capture it.
export type TraceGap = { from: [number, number]; to: [number, number] };

export function splitTraceOnGaps<T extends TracePoint>(pts: T[]): {
  segments: T[][];
  gaps: TraceGap[];
} {
  if (pts.length === 0) return { segments: [], gaps: [] };
  const segs: T[][] = [[pts[0]]];
  const gaps: TraceGap[] = [];
  for (let i = 1; i < pts.length; i++) {
    const cur = pts[i];
    const prev = pts[i - 1];
    const dt =
      cur.t != null && prev.t != null ? cur.t - prev.t : 0;
    const dist = haversine(prev, cur);
    if (dt >= GAP_TIME_S || dist >= GAP_DIST_M) {
      gaps.push({ from: [prev.lat, prev.lon], to: [cur.lat, cur.lon] });
      segs.push([cur]);
    } else {
      segs[segs.length - 1].push(cur);
    }
  }
  // A segment that ended up as a single sample isn't drawable as a
  // line; drop it but keep the surrounding gaps intact.
  return {
    segments: segs.filter((s) => s.length >= 2),
    gaps,
  };
}

// classifySegment scores how much we trust a map-matcher to render
// this stretch faithfully. Short, low-velocity, sample-sparse
// segments are best rendered as the raw GPS polyline — snapping
// hallucinates a route along whatever road the matcher picks
// nearby, and the result is worse than the actual recorded trace.
export type SegmentQuality = "trivial" | "short" | "normal";

export function classifySegment<T extends TracePoint>(seg: T[]): SegmentQuality {
  if (seg.length < 5) return "trivial";
  let dist = 0;
  for (let i = 1; i < seg.length; i++) {
    dist += haversine(seg[i - 1], seg[i]);
  }
  if (dist < 200) return "trivial";
  if (dist < 800) return "short";
  return "normal";
}
