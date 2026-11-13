import type { ElevationProfile } from "../lib/api";
import type { DistanceUnit } from "../lib/preferences";

// Pure parts of the elevation chart: the profile's shape in chart units, the cursor's place on it, and the same place on the map's line.
// The cursor is a share (0 to 1) of the way along the route, so the chart (server distances) and the map (the drawn line, which may be snapped a
// little differently) agree on "how far" without sharing a length.
export type LatLng = [number, number];

export const FT_PER_M = 3.28084;

export const elevationUnit = (u: DistanceUnit): "ft" | "m" => (u === "mi" ? "ft" : "m");
export const elevationIn = (m: number, u: DistanceUnit): number => (u === "mi" ? m * FT_PER_M : m);

// A profile worth drawing: two points or more that run forward in distance. Anything else draws no chart.
export function usableProfile(p: ElevationProfile | undefined): ElevationProfile | null {
  if (!p || !Array.isArray(p.points) || p.points.length < 2) return null;
  for (let i = 0; i < p.points.length; i++) {
    const [d, e] = p.points[i];
    if (!Number.isFinite(d) || !Number.isFinite(e)) return null;
    if (i > 0 && d < p.points[i - 1][0]) return null;
  }
  return p.points[p.points.length - 1][0] > 0 ? p : null;
}

export const totalMeters = (p: ElevationProfile): number => p.points[p.points.length - 1][0];

// heightAt is the terrain height (metres) at a share of the way along, interpolated between the two points around it.
export function heightAt(p: ElevationProfile, share: number): number {
  const pts = p.points;
  const d = Math.min(1, Math.max(0, share)) * totalMeters(p);
  let lo = 0;
  let hi = pts.length - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (pts[mid][0] <= d) lo = mid;
    else hi = mid;
  }
  const [d0, e0] = pts[lo];
  const [d1, e1] = pts[hi];
  return d1 === d0 ? e0 : e0 + ((e1 - e0) * (d - d0)) / (d1 - d0);
}

// The shares where the stops sit (null for one that could not be placed), without the first and last waypoint.
export function stopShares(p: ElevationProfile): { index: number; share: number }[] {
  const total = totalMeters(p);
  const out: { index: number; share: number }[] = [];
  p.waypoint_at_m.forEach((m, index) => {
    if (m == null || index === 0 || index === p.waypoint_at_m.length - 1) return;
    out.push({ index, share: Math.min(1, Math.max(0, m / total)) });
  });
  return out;
}

const R = 6371008.8;
const rad = (d: number) => (d * Math.PI) / 180;
// Equirectangular metres between two points: exact enough along one route, and cheap.
function segMeters(a: LatLng, b: LatLng): number {
  const x = rad(b[1] - a[1]) * Math.cos(rad((a[0] + b[0]) / 2));
  const y = rad(b[0] - a[0]);
  return R * Math.hypot(x, y);
}

export function cumulative(path: LatLng[]): number[] {
  const out = [0];
  for (let i = 1; i < path.length; i++) out.push(out[i - 1] + segMeters(path[i - 1], path[i]));
  return out;
}

// pointAtShare is the map position a share of the way along the line, or null for a line too short to have one.
// cum is the line's cumulative metres when the caller has them already (one pointer move after another asks again and again).
export function pointAtShare(path: LatLng[], share: number, cum: number[] = cumulative(path)): LatLng | null {
  if (path.length < 2) return null;
  const len = cum[cum.length - 1];
  if (!(len > 0)) return null;
  const d = Math.min(1, Math.max(0, share)) * len;
  let i = 1;
  while (i < cum.length - 1 && cum[i] < d) i++;
  const span = cum[i] - cum[i - 1];
  const t = span > 0 ? (d - cum[i - 1]) / span : 0;
  return [path[i - 1][0] + (path[i][0] - path[i - 1][0]) * t, path[i - 1][1] + (path[i][1] - path[i - 1][1]) * t];
}

// shareOfPoint is the share of the way along the line of the point on it nearest to p, and how far p is from the line (metres).
export function shareOfPoint(path: LatLng[], p: LatLng): { share: number; offMeters: number } | null {
  if (path.length < 2) return null;
  const cum = cumulative(path);
  const len = cum[cum.length - 1];
  if (!(len > 0)) return null;
  const k = Math.cos(rad(p[0]));
  let best = { along: 0, off: Infinity };
  for (let i = 1; i < path.length; i++) {
    const ax = rad(path[i - 1][1] - p[1]) * k * R;
    const ay = rad(path[i - 1][0] - p[0]) * R;
    const bx = rad(path[i][1] - p[1]) * k * R;
    const by = rad(path[i][0] - p[0]) * R;
    const dx = bx - ax;
    const dy = by - ay;
    const l2 = dx * dx + dy * dy;
    const t = l2 > 0 ? Math.min(1, Math.max(0, -(ax * dx + ay * dy) / l2)) : 0;
    const off = Math.hypot(ax + dx * t, ay + dy * t);
    if (off < best.off) best = { along: cum[i - 1] + (cum[i] - cum[i - 1]) * t, off };
  }
  return { share: best.along / len, offMeters: best.off };
}
