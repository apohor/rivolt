// The road-snapping pipeline of the drive map: a recorded GPS trace is cleaned, split on gaps, matched to roads through the same-origin
// Valhalla proxy (/api/maps/valhalla), and the gaps the matcher gave up on are filled with routed segments where it can. Pure data in, data
// out (no map library): both the MapLibre and the older Leaflet maps draw its result. Coordinates are [lat, lon] throughout.
import { decodePolyline } from "../lib/polyline";
import { valhallaBase, ensureConfig } from "../lib/config";
import { cleanTrace, classifySegment, splitTraceOnGaps, haversine, type TraceGap } from "../lib/trace";

// SnapPoint is the per-sample shape consumed by the snap pipeline.
// Caller fills in {lat, lon}; t (unix seconds) and s (speed mph) are
// optional and only used to help the matcher / colour the polyline.
export type SnapPoint = { lat: number; lon: number; t?: number; s?: number };

// valhallaRoutePair returns a road-routed polyline between two coords
// or null on failure. Used to fill gaps where map-matching gave up
// (between sub-matchings, or across true GPS dropouts) with a path
// that follows actual roads instead of a straight chord.
async function valhallaRoutePair(
  from: [number, number],
  to: [number, number],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "") return null;
  const body = {
    locations: [
      { lat: from[0], lon: from[1], type: "break" },
      { lat: to[0], lon: to[1], type: "break" },
    ],
    costing: "auto",
    directions_options: { units: "miles" },
  };
  try {
    const r = await fetch(`${base}/route`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      trip?: { legs?: { shape?: string }[] };
    };
    const legs = j.trip?.legs ?? [];
    if (legs.length === 0) return null;
    const out: [number, number][] = [];
    for (let i = 0; i < legs.length; i++) {
      const seg = decodePolyline(legs[i].shape, 6);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// valhallaSnap calls /trace_route with walk_or_snap in OSRM-compatible
// response format. Returns an ordered list of snapped sub-segments
// plus dashed gap connectors for the input ranges Valhalla couldn't
// match continuously. Walk_or_snap routinely splits a noisy trace
// into 2+ `matchings` — the previous Valhalla-native response shape
// surfaced only the longest match in `trip` and buried the rest in
// `alternates`, which we'd silently drop. OSRM format gives us the
// `tracepoints` array (one entry per input shape point) mapping each
// input point to its matching index, which is how we know where the
// matcher gave up.
async function valhallaSnap(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<{ chunks: [number, number][][]; gaps: TraceGap[] } | null> {
  const base = valhallaBase();
  if (base === "" || pts.length < 2) return null;
  const MAX_TRACE = 1500;
  let trace = pts;
  if (trace.length > MAX_TRACE) {
    const step = Math.ceil(trace.length / MAX_TRACE);
    trace = trace.filter((_, i) => i % step === 0);
    if (trace[trace.length - 1] !== pts[pts.length - 1]) {
      trace.push(pts[pts.length - 1]);
    }
  }
  const body = {
    shape: trace.map((p) =>
      Number.isFinite(p.t)
        ? { lat: p.lat, lon: p.lon, time: p.t as number }
        : { lat: p.lat, lon: p.lon },
    ),
    costing: "auto",
    shape_match: "walk_or_snap",
    trace_options: { search_radius: 100, gps_accuracy: 25 },
    format: "osrm",
  };
  try {
    const r = await fetch(`${base}/trace_route`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      code?: string;
      matchings?: { geometry?: string; confidence?: number }[];
      tracepoints?: ({ matchings_index: number } | null)[];
    };
    const matchings = j.matchings ?? [];
    const tps = j.tracepoints ?? [];
    if (matchings.length === 0) return null;
    // OSRM polylines from Valhalla are precision-6.
    const chunks: [number, number][][] = matchings.map((m) =>
      m.geometry ? decodePolyline(m.geometry, 6) : [],
    );
    // Walk the tracepoints to find each matching's input-span boundaries.
    // The last input point of matching N and the first input point of
    // matching N+1 frame the unmatched stretch we render as a dashed
    // connector. We use the input coords (not the snapped endpoints)
    // because the gap really lives in input-space — Valhalla doesn't
    // emit a polyline for it.
    const lastInputOf: number[] = new Array(matchings.length).fill(-1);
    const firstInputOf: number[] = new Array(matchings.length).fill(-1);
    for (let i = 0; i < tps.length; i++) {
      const tp = tps[i];
      if (!tp) continue;
      const k = tp.matchings_index;
      if (k < 0 || k >= matchings.length) continue;
      if (firstInputOf[k] === -1) firstInputOf[k] = i;
      lastInputOf[k] = i;
    }
    const gaps: TraceGap[] = [];
    for (let k = 1; k < matchings.length; k++) {
      const prevEnd = lastInputOf[k - 1];
      const curStart = firstInputOf[k];
      if (prevEnd < 0 || curStart < 0) continue;
      const a = trace[prevEnd];
      const b = trace[curStart];
      gaps.push({ from: [a.lat, a.lon], to: [b.lat, b.lon] });
    }
    const valid = chunks.filter((c) => c.length > 1);
    return valid.length > 0 ? { chunks: valid, gaps } : null;
  } catch {
    return null;
  }
}

export type SnapPlan = {
  segments: { coords: [number, number][]; raw: boolean }[];
  // Gaps the renderer couldn't fill with a routed polyline — drawn
  // as dashed neutral connectors so the user can tell the matcher
  // gave up here.
  gaps: TraceGap[];
  // Routed gap-fills: where the matcher gave up between sub-matches
  // (or between split segments) but Valhalla's /route engine could
  // produce a road-following polyline between the endpoints. Drawn
  // as solid neutral lines so the eye reads them as "inferred path"
  // rather than recorded telemetry.
  routedGaps: [number, number][][];
};

// Render the raw GPS chord polyline for a segment we don't want to
// trust the matcher with — parking-lot maneuvers, sub-200m drives,
// trace-too-short cases. The raw trace IS the actual recorded path,
// snapping only adds hallucination.
export function rawPolyline(seg: SnapPoint[]): [number, number][] {
  return seg.map((p) => [p.lat, p.lon]);
}

export async function snapToRoads(
  rawPoints: SnapPoint[],
  signal: AbortSignal,
): Promise<SnapPlan | null> {
  const points = cleanTrace(rawPoints);
  // Fallback to the unfiltered raw points when cleaning leaves too
  // little to work with — a sparsely-sampled drive should still
  // render a polyline rather than nothing at all.
  if (points.length < 2) {
    const raw = rawPoints.filter(
      (p) => Number.isFinite(p.lat) && Number.isFinite(p.lon),
    );
    if (raw.length < 2) return null;
    return {
      segments: [{ coords: rawPolyline(raw), raw: true }],
      gaps: [],
      routedGaps: [],
    };
  }
  await ensureConfig();
  if (signal.aborted) return null;

  const { segments, gaps } = splitTraceOnGaps(points);
  if (segments.length === 0) {
    return {
      segments: [{ coords: rawPolyline(points), raw: true }],
      gaps: [],
      routedGaps: [],
    };
  }

  const out: SnapPlan = { segments: [], gaps, routedGaps: [] };
  const valhalla = valhallaBase() !== "";

  for (const seg of segments) {
    if (signal.aborted) return null;
    const quality = classifySegment(seg);
    if (quality === "trivial") {
      out.segments.push({ coords: rawPolyline(seg), raw: true });
      continue;
    }
    let chunks: [number, number][][] | null = null;
    let innerGaps: TraceGap[] = [];
    if (valhalla) {
      const v = await valhallaSnap(seg, signal);
      if (v) {
        chunks = v.chunks;
        innerGaps = v.gaps;
      }
    }
    if (signal.aborted) return null;
    if (chunks && chunks.length > 0) {
      // Push each snapped sub-segment as its own out.segment so the
      // renderer can draw dashed connectors between them. innerGaps
      // (Valhalla telling us where it gave up matching) bubble up to
      // out.gaps alongside the trace-level gaps from splitTraceOnGaps.
      for (const c of chunks) out.segments.push({ coords: c, raw: false });
      out.gaps.push(...innerGaps);
      // Head/tail coverage: the first matching may not start at the
      // segment's first input point, and the last may not end at the
      // last input point. Wrap with raw chords so the full recorded
      // trace remains visible end-to-end.
      const TAIL_TOLERANCE_M = 200;
      const firstSnap = chunks[0][0];
      const headDist = haversine(
        { lat: seg[0].lat, lon: seg[0].lon },
        { lat: firstSnap[0], lon: firstSnap[1] },
      );
      if (headDist > TAIL_TOLERANCE_M) {
        out.gaps.push({
          from: [seg[0].lat, seg[0].lon],
          to: firstSnap,
        });
      }
      const lastSnap = chunks[chunks.length - 1][chunks[chunks.length - 1].length - 1];
      const tailPt = seg[seg.length - 1];
      const tailDist = haversine(
        { lat: lastSnap[0], lon: lastSnap[1] },
        { lat: tailPt.lat, lon: tailPt.lon },
      );
      if (tailDist > TAIL_TOLERANCE_M) {
        out.gaps.push({
          from: lastSnap,
          to: [tailPt.lat, tailPt.lon],
        });
      }
    } else {
      // Snap failure: render the raw chord — better than nothing.
      out.segments.push({ coords: rawPolyline(seg), raw: true });
    }
  }

  // Route-fill the gaps. For each unfilled gap, ask Valhalla's /route
  // engine for a road-following polyline between the endpoints. On
  // success the gap becomes a routedGaps polyline (rendered solid +
  // neutral so it reads as "inferred path") and the dashed connector
  // is dropped. On failure we keep the dashed gap so the user can
  // still tell the matcher gave up here. Issued in parallel since
  // each /route call is independent.
  if (valhalla && out.gaps.length > 0) {
    const filled = await Promise.all(
      out.gaps.map((g) => valhallaRoutePair(g.from, g.to, signal)),
    );
    if (signal.aborted) return null;
    const remaining: TraceGap[] = [];
    for (let i = 0; i < out.gaps.length; i++) {
      const routed = filled[i];
      if (routed && routed.length > 1) {
        out.routedGaps.push(routed);
      } else {
        remaining.push(out.gaps[i]);
      }
    }
    out.gaps = remaining;
  }

  return out.segments.length > 0 ? out : null;
}
