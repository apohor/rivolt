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

const LAYERS = ["rivolt-route-glow", "rivolt-route-main", "rivolt-route-routed", "rivolt-route-gap"];

// setRoute puts the route on the map, creating its source and layers on first use and replacing the data after. Safe to call again after a
// style change (setStyle drops added layers): it re-creates what is missing.
export function setRoute(map: MLMap, data: RouteData, flavor: "dark" | "light" = "dark"): void {
  const src = map.getSource(ROUTE_SOURCE) as GeoJSONSource | undefined;
  if (src) {
    src.setData(data as never);
  } else {
    map.addSource(ROUTE_SOURCE, { type: "geojson", data: data as never });
  }
  const round = { "line-cap": "round", "line-join": "round" } as const;
  if (!map.getLayer(LAYERS[0])) {
    map.addLayer({ id: LAYERS[0], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "speed"], layout: round, paint: { "line-color": ROUTE_GLOW, "line-width": flavor === "light" ? 4.5 : 9, "line-opacity": flavor === "light" ? 0.25 : 0.18 } });
    map.addLayer({ id: LAYERS[1], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "speed"], layout: round, paint: { "line-color": ["get", "color"], "line-width": 3, "line-opacity": 0.95 } });
    map.addLayer({ id: LAYERS[2], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "routed"], layout: round, paint: { "line-color": ROUTE_NEUTRAL, "line-width": 2.5, "line-opacity": 0.75 } });
    map.addLayer({ id: LAYERS[3], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "gap"], paint: { "line-color": ROUTE_NEUTRAL, "line-width": 2, "line-opacity": 0.7, "line-dasharray": [3, 3] } });
  }
}

// ---- the drive map's route: one gradient-coloured track, with the dashed/routed fills over it ----

export const TRACK_SOURCE = "rivolt-track";
const DRIVE_LAYERS = ["rivolt-track-glow", "rivolt-track-casing", "rivolt-track-main", "rivolt-drive-gapcase", "rivolt-drive-routed", "rivolt-drive-gap"];
const CASING = { dark: "#06080C", light: "#FFFFFF" } as const;

export type DriveRoute = {
  // The whole track in order, [lat, lon]: the recorded trace, or the matched segments joined end to end.
  track: LatLng[];
  // A line-gradient expression's [progress, colour, ...] stops along the track (metrics.gradientStops).
  stops: (number | string)[];
  // Where the match gave up: road-routed fills (solid) and true dropouts (dashed), drawn neutral over the track.
  routedGaps?: LatLng[][];
  gaps?: { from: LatLng; to: LatLng }[];
};

const gradientExpr = (stops: (number | string)[]) => ["interpolate", ["linear"], ["line-progress"], ...stops] as never;

// setDriveRoute puts the drive's route on the map, creating its sources and layers on first use and replacing the data and gradient after. Safe to
// call again after a style change. The layers go below `beforeId` (the basemap's first label) so place names stay readable over the line.
export function setDriveRoute(map: MLMap, r: DriveRoute, flavor: "dark" | "light", beforeId?: string): void {
  const trackData = {
    type: "FeatureCollection" as const,
    features: r.track.length >= 2 ? [{ type: "Feature" as const, geometry: { type: "LineString" as const, coordinates: r.track.map(lonlat) }, properties: {} }] : [],
  };
  const fills = routeData({ runs: [], routedGaps: r.routedGaps, gaps: r.gaps });
  const ts = map.getSource(TRACK_SOURCE) as GeoJSONSource | undefined;
  if (ts) ts.setData(trackData as never);
  else map.addSource(TRACK_SOURCE, { type: "geojson", lineMetrics: true, data: trackData as never });
  const rs = map.getSource(ROUTE_SOURCE) as GeoJSONSource | undefined;
  if (rs) rs.setData(fills as never);
  else map.addSource(ROUTE_SOURCE, { type: "geojson", data: fills as never });
  const round = { "line-cap": "round", "line-join": "round" } as const;
  const grad = gradientExpr(r.stops);
  if (!map.getLayer(DRIVE_LAYERS[0])) {
    const dark = flavor === "dark";
    map.addLayer({ id: DRIVE_LAYERS[0], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 14, "line-blur": 9, "line-opacity": dark ? 0.5 : 0.3, "line-gradient": grad } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[1], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 8, "line-color": CASING[flavor], "line-opacity": 0.92 } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[2], type: "line", source: TRACK_SOURCE, layout: round, paint: { "line-width": 4.6, "line-gradient": grad } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[3], type: "line", source: ROUTE_SOURCE, filter: ["in", ["get", "kind"], ["literal", ["routed", "gap"]]], layout: round, paint: { "line-width": 7, "line-color": CASING[flavor], "line-opacity": 0.92 } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[4], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "routed"], layout: round, paint: { "line-color": ROUTE_NEUTRAL, "line-width": 2.5, "line-opacity": 0.8 } }, beforeId);
    map.addLayer({ id: DRIVE_LAYERS[5], type: "line", source: ROUTE_SOURCE, filter: ["==", ["get", "kind"], "gap"], paint: { "line-color": ROUTE_NEUTRAL, "line-width": 2, "line-opacity": 0.8, "line-dasharray": [3, 3] } }, beforeId);
  } else {
    setDriveGradient(map, r.stops);
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
