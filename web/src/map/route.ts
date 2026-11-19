// The drive route as map data: GeoJSON built from a recorded or road-snapped trace, and the layers that draw it (a soft glow under a crisp
// line coloured by speed band, routed gap-fills in neutral, dashed connectors where nothing connects). Pure data, then one function that adds
// the source and layers to a MapLibre map; the layers go on top of whatever style is loaded and are re-added after a style change.
import type { GeoJSONSource, Map as MLMap } from "maplibre-gl";
import { SPEED_BUCKETS, speedBucketIndex } from "../lib/speedBuckets";

export type LatLng = [number, number]; // [lat, lon] as the rest of the app holds them

export type RouteFeature = {
  type: "Feature";
  geometry: { type: "LineString"; coordinates: [number, number][] }; // [lon, lat], GeoJSON order
  properties: { kind: "speed" | "routed" | "gap"; color: string };
};
export type RouteData = { type: "FeatureCollection"; features: RouteFeature[] };

export const ROUTE_SOURCE = "rivolt-route";
export const ROUTE_UNIFORM = "#34d399";
export const ROUTE_GLOW = "#10b981";
export const ROUTE_NEUTRAL = "#737373";

export function speedColor(mph: number | undefined): string {
  if (mph == null || !Number.isFinite(mph)) return ROUTE_UNIFORM;
  return SPEED_BUCKETS[speedBucketIndex(mph)].color;
}

const lonlat = (p: LatLng): [number, number] => [p[1], p[0]];

// speedRuns cuts a trace into runs of one speed band, each sharing its end point with the next so the line stays continuous. Without
// per-point speeds it is one uniform run.
export function speedRuns(latlngs: LatLng[], speeds?: (number | undefined)[]): RouteFeature[] {
  if (latlngs.length < 2) return [];
  const hasSpeeds = !!speeds && speeds.length === latlngs.length && speeds.some((s) => s != null && Number.isFinite(s));
  const feature = (pts: LatLng[], color: string): RouteFeature => ({
    type: "Feature",
    geometry: { type: "LineString", coordinates: pts.map(lonlat) },
    properties: { kind: "speed", color },
  });
  if (!hasSpeeds) return [feature(latlngs, ROUTE_UNIFORM)];
  const out: RouteFeature[] = [];
  let start = 0;
  let color = speedColor(speeds![0]);
  for (let i = 1; i < latlngs.length; i++) {
    const c = speedColor(speeds![i]);
    if (c !== color) {
      out.push(feature(latlngs.slice(start, i + 1), color));
      start = i;
      color = c;
    }
  }
  out.push(feature(latlngs.slice(start), color));
  return out;
}

export function routeData(parts: { runs: RouteFeature[]; routedGaps?: LatLng[][]; gaps?: { from: LatLng; to: LatLng }[] }): RouteData {
  const features = [...parts.runs];
  for (const r of parts.routedGaps ?? []) {
    if (r.length > 1) features.push({ type: "Feature", geometry: { type: "LineString", coordinates: r.map(lonlat) }, properties: { kind: "routed", color: ROUTE_NEUTRAL } });
  }
  for (const g of parts.gaps ?? []) {
    features.push({ type: "Feature", geometry: { type: "LineString", coordinates: [lonlat(g.from), lonlat(g.to)] }, properties: { kind: "gap", color: ROUTE_NEUTRAL } });
  }
  return { type: "FeatureCollection", features };
}

// bounds of every coordinate: [west, south, east, north], or null when there is nothing finite.
export function boundsOf(points: LatLng[]): [[number, number], [number, number]] | null {
  let w = Infinity, s = Infinity, e = -Infinity, n = -Infinity;
  for (const [la, lo] of points) {
    if (!Number.isFinite(la) || !Number.isFinite(lo)) continue;
    w = Math.min(w, lo); e = Math.max(e, lo); s = Math.min(s, la); n = Math.max(n, la);
  }
  return Number.isFinite(w) ? [[w, s], [e, n]] : null;
}

// ---- the drive map's route: one gradient-coloured track, with the dashed/routed fills over it ----

export const TRACK_SOURCE = "rivolt-track";
const DRIVE_LAYERS = ["rivolt-track-glow", "rivolt-track-casing", "rivolt-track-main", "rivolt-drive-gapcase", "rivolt-drive-routed", "rivolt-drive-gap"];
export const CASING = { dark: "#06080C", light: "#FFFFFF" } as const;

export type DriveRoute = {
  // The whole track in order, [lat, lon]: the recorded trace, or the matched segments joined end to end.
  track: LatLng[];
  // A line-gradient expression's [progress, colour, ...] stops along the track (metrics.gradientStops).
  stops: (number | string)[];
  // Where the match gave up: road-routed fills (solid) and true dropouts (dashed), drawn neutral over the track.
  routedGaps?: LatLng[][];
  gaps?: { from: LatLng; to: LatLng }[];
  // gap_dim: where the dropouts fall along the track, as [from, to] line progress. When set, the track is cleared there and each dropout is
  // drawn solid in the colour the route has at that point, faded, instead of the neutral dashes.
  gapRanges?: [number, number][];
};

// Whether a map's drive layers were last built with the dropouts dimmed.
const dimmed = new WeakMap<object, boolean>();
const CLEAR = "rgba(0,0,0,0)";
const EDGE = 1e-7;

// colorAtProgress is the colour a [progress, colour, ...] stop list gives at p (the last stop at or before it: steps are good enough for a fade).
export function colorAtProgress(stops: (number | string)[], p: number): string {
  let c = String(stops[1] ?? ROUTE_NEUTRAL);
  for (let i = 0; i + 1 < stops.length; i += 2) {
    if (Number(stops[i]) <= p) c = String(stops[i + 1]);
  }
  return c;
}

// clearRanges returns the stops with each [from, to] progress range made fully transparent, with hard edges.
export function clearRanges(stops: (number | string)[], ranges: [number, number][]): (number | string)[] {
  const rs = ranges.filter(([a, b]) => b - a > 4 * EDGE).sort((x, y) => x[0] - y[0]);
  if (rs.length === 0) return stops;
  const out: (number | string)[] = [];
  let last = -1;
  const push = (p: number, c: string) => {
    if (p <= last) return;
    out.push(p, c);
    last = p;
  };
  let k = 0;
  for (let i = 0; i + 1 < stops.length; i += 2) {
    const p = Number(stops[i]);
    while (k < rs.length && rs[k][1] <= p) {
      const [a, b] = rs[k++];
      push(a, colorAtProgress(stops, a));
      push(a + EDGE, CLEAR);
      push(b - EDGE, CLEAR);
      push(b, colorAtProgress(stops, b));
    }
    if (rs.slice(k).some(([a, b]) => p > a && p < b)) continue;
    push(p, String(stops[i + 1]));
  }
  while (k < rs.length) {
    const [a, b] = rs[k++];
    push(a, colorAtProgress(stops, a));
    push(a + EDGE, CLEAR);
    push(b - EDGE, CLEAR);
    push(b, colorAtProgress(stops, b));
  }
  return out;
}

// chainGaps joins dropouts that meet end to start (a long hole with sparse samples is cut into one gap per sample pair) into one line each,
// with the index of the first gap of the chain. Drawn separately, round caps overlap at the faded opacity and leave a darker dot at every joint.
export function chainGaps(gaps: { from: LatLng; to: LatLng }[]): { coords: LatLng[]; first: number }[] {
  const same = (a: LatLng, b: LatLng) => Math.abs(a[0] - b[0]) < 1e-9 && Math.abs(a[1] - b[1]) < 1e-9;
  const out: { coords: LatLng[]; first: number }[] = [];
  gaps.forEach((g, i) => {
    const last = out[out.length - 1];
    if (last && same(last.coords[last.coords.length - 1], g.from)) last.coords.push(g.to);
    else out.push({ coords: [g.from, g.to], first: i });
  });
  return out;
}

// gapProgress locates each dropout on the track as [from, to] line progress, by the track vertices nearest its ends.
export function gapProgress(track: LatLng[], gaps: { from: LatLng; to: LatLng }[]): [number, number][] {
  if (track.length < 2) return [];
  const cum = [0];
  for (let i = 1; i < track.length; i++) {
    const dLat = track[i][0] - track[i - 1][0];
    const dLon = (track[i][1] - track[i - 1][1]) * Math.cos((track[i][0] * Math.PI) / 180);
    cum.push(cum[i - 1] + Math.hypot(dLat, dLon));
  }
  const total = cum[cum.length - 1];
  if (!(total > 0)) return [];
  // The vertices near the least distance from q (within 1.5x, or 50 m more): on a drive that passes a place twice, both passes qualify.
  const SLACK = 50 / 111320;
  const candidates = (q: LatLng) => {
    const d = track.map((t) => Math.hypot(t[0] - q[0], (t[1] - q[1]) * Math.cos((q[0] * Math.PI) / 180)));
    const least = Math.min(...d);
    const out: number[] = [];
    d.forEach((x, i) => { if (x <= Math.max(least * 1.5, least + SLACK) + 1e-9) out.push(i); });
    return out;
  };
  // A dropout joins two vertices in drive order: of the pairs (from at i, to at j >= i) take the shortest stretch of track between them.
  return gaps.map((g) => {
    const from = candidates(g.from);
    const to = candidates(g.to);
    let best: [number, number] | null = null;
    for (const a of from) {
      for (const b of to) {
        const [lo, hi] = a <= b ? [a, b] : [b, a];
        if (best === null || cum[hi] - cum[lo] < cum[best[1]] - cum[best[0]]) best = [lo, hi];
      }
    }
    const [lo, hi] = best ?? [0, 0];
    return [cum[lo] / total, cum[hi] / total] as [number, number];
  });
}

const gradientExpr = (stops: (number | string)[]) => ["interpolate", ["linear"], ["line-progress"], ...stops] as never;

// setDriveRoute puts the drive's route on the map, creating its sources and layers on first use and replacing the data and gradient after. Safe to
// call again after a style change. The layers go below `beforeId` (the basemap's first label) so place names stay readable over the line.
export function setDriveRoute(map: MLMap, r: DriveRoute, flavor: "dark" | "light", beforeId?: string): void {
  const trackData = {
    type: "FeatureCollection" as const,
    features: r.track.length >= 2 ? [{ type: "Feature" as const, geometry: { type: "LineString" as const, coordinates: r.track.map(lonlat) }, properties: {} }] : [],
  };
  const dim = !!r.gapRanges && r.gapRanges.length > 0;
  const fills = routeData({ runs: [], routedGaps: r.routedGaps, gaps: r.gaps });
  if (dim) {
    // One line per run of chained dropouts, in the colour where its first one is.
    fills.features = fills.features.filter((x) => x.properties.kind !== "gap");
    for (const c of chainGaps(r.gaps ?? [])) {
      const range = r.gapRanges![c.first];
      fills.features.push({ type: "Feature", geometry: { type: "LineString", coordinates: c.coords.map(lonlat) }, properties: { kind: "gap", color: range ? colorAtProgress(r.stops, (range[0] + range[1]) / 2) : ROUTE_NEUTRAL } });
    }
  }
  const ts = map.getSource(TRACK_SOURCE) as GeoJSONSource | undefined;
  if (ts) ts.setData(trackData as never);
  else map.addSource(TRACK_SOURCE, { type: "geojson", lineMetrics: true, data: trackData as never });
  const rs = map.getSource(ROUTE_SOURCE) as GeoJSONSource | undefined;
  if (rs) rs.setData(fills as never);
  else map.addSource(ROUTE_SOURCE, { type: "geojson", data: fills as never });
  const round = { "line-cap": "round", "line-join": "round" } as const;
  const cleared = dim ? clearRanges(r.stops, r.gapRanges!) : r.stops;
  const grad = gradientExpr(cleared);
  const casingGrad = dim ? gradientExpr(clearRanges([0, CASING[flavor], 1, CASING[flavor]], r.gapRanges!)) : undefined;
  // The layers are made before the dropouts are known (they come with the road match), so a change in whether any are dimmed rebuilds them.
  if (map.getLayer(DRIVE_LAYERS[5]) && dim !== (dimmed.get(map) ?? false)) {
    for (const id of [...DRIVE_LAYERS].reverse()) if (map.getLayer(id)) map.removeLayer(id);
  }
  dimmed.set(map, dim);
  if (!map.getLayer(DRIVE_LAYERS[0])) {
    const dark = flavor === "dark";
    map.addLayer({ id: DRIVE_LAYERS[0], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 14, "line-blur": 9, "line-opacity": dark ? 0.5 : 0.3, "line-gradient": grad } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[1], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 8, ...(casingGrad ? { "line-gradient": casingGrad } : { "line-color": CASING[flavor] }), "line-opacity": 0.92 } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[2], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 4.6, "line-gradient": grad } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[3], type: "line", source: ROUTE_SOURCE, filter: dim ? ["==", ["get", "kind"], "routed"] : ["in", ["get", "kind"], ["literal", ["routed", "gap"]]], layout: round, paint: { "line-width": 7, "line-color": CASING[flavor], "line-opacity": 0.92 } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[4], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "routed"], layout: round, paint: { "line-color": ROUTE_NEUTRAL, "line-width": 2.5, "line-opacity": 0.8 } }, beforeId);
    map.addLayer({
      id: DRIVE_LAYERS[5], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "gap"],
      ...(dim ? { layout: round } : {}),
      paint: dim ? { "line-color": ["get", "color"], "line-width": 4.6, "line-opacity": 0.35 } : { "line-color": ROUTE_NEUTRAL, "line-width": 2, "line-opacity": 0.8, "line-dasharray": [3, 3] },
    } as never, beforeId);
  } else {
    setDriveGradient(map, cleared);
  }
}

// setDriveGradient recolours the track (a colour-mode switch) without touching its geometry.
export function setDriveGradient(map: MLMap, stops: (number | string)[]): void {
  const grad = gradientExpr(stops);
  for (const id of [DRIVE_LAYERS[0], DRIVE_LAYERS[2]]) {
    if (map.getLayer(id)) map.setPaintProperty(id, "line-gradient", grad);
  }
}

// firstLabelLayer is the id of the style's first symbol layer, so the route can go under the place names.
export function firstLabelLayer(map: MLMap): string | undefined {
  return map.getStyle()?.layers?.find((l) => l.type === "symbol")?.id;
}
