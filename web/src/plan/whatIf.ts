import type { PlannedWaypoint, TripRoute, WhatIfRequest, WhatIfResponse, WhatIfStop } from "../lib/api";
import { formatDistance, type DistanceUnit } from "../lib/preferences";
import { decodePolyline6 } from "../map/tripPure";

// The what-if preview of adding a stop: our own router's estimate, shown before anything is committed. The change leads (time, distance), a clock time
// is secondary, the battery is a change only, warnings are one-way. The Rivian re-plan only happens when the person adds the stop.
export const WHAT_IF_NOTICE = "Estimate. Rivian will re-plan and may change charging stops and times.";
export const WHAT_IF_ROUGH = "Rough estimate: the routing was unavailable, so this is a straight-line guess.";

const SOURCES = ["origin", "via", "planner", "overnight", "destination"] as const;

export function whatIfStops(route: TripRoute): WhatIfStop[] {
  const ws: PlannedWaypoint[] = route.Waypoints.filter((w) => Number.isFinite(w.Latitude) && Number.isFinite(w.Longitude));
  const n = ws.length;
  return ws.map((w, i) => {
    const source = w.Source && SOURCES.includes(w.Source) ? w.Source : i === 0 ? "origin" : i === n - 1 ? "destination" : w.ChargeDurationSec > 0 ? "planner" : "via";
    const s: WhatIfStop = { lat: w.Latitude, lon: w.Longitude, source };
    if (w.ArrivalTimeUTC) s.arrival_at = w.ArrivalTimeUTC;
    if (w.DepartureTimeUTC) s.departure_at = w.DepartureTimeUTC;
    if (i > 0 && w.ArrivalSoC > 0) s.arrival_soc = w.ArrivalSoC;
    if (i < n - 1 && w.DepartureSoC > 0) s.departure_soc = w.DepartureSoC;
    if (w.DistanceFromOriginMeters) s.distance_from_origin_m = w.DistanceFromOriginMeters;
    return s;
  });
}

function nearestVertex(line: [number, number][], lat: number, lon: number): number {
  let best = 0;
  let bd = Infinity;
  const k = Math.cos((lat * Math.PI) / 180);
  for (let i = 0; i < line.length; i++) {
    const dy = line[i][0] - lat;
    const dx = (line[i][1] - lon) * k;
    const d = dx * dx + dy * dy;
    if (d < bd) { bd = d; best = i; }
  }
  return best;
}

// Where a new stop goes: before the first stop the road line reaches after the point (never before the origin, always before the destination).
export function insertionIndex(stops: WhatIfStop[], point: { lat: number; lon: number }, line: [number, number][]): number {
  if (stops.length < 2) return 1;
  if (line.length < 2) return stops.length - 1;
  const at = nearestVertex(line, point.lat, point.lon);
  for (let i = 1; i < stops.length; i++) if (nearestVertex(line, stops[i].lat, stops[i].lon) > at) return i;
  return stops.length - 1;
}

export function addRequest(route: TripRoute, point: { lat: number; lon: number }): WhatIfRequest | null {
  const stops = whatIfStops(route);
  if (stops.length < 2) return null;
  const line = decodePolyline6(route.Geometry ?? route.route_polyline);
  return { stops, edit: { kind: "add", index: insertionIndex(stops, point, line), via: { lat: point.lat, lon: point.lon } } };
}

// "+12 min", "−5 min", "+1 h 5 min".
export function signedDuration(sec: number | null | undefined): string | null {
  if (sec == null || !Number.isFinite(sec)) return null;
  const minutes = Math.round(Math.abs(sec) / 60);
  const body = minutes >= 60 ? `${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ""}` : `${minutes} min`;
  return (sec < 0 ? "−" : "+") + body;
}

export function whatIfLines(r: WhatIfResponse, unit: DistanceUnit, zone?: string): string[] {
  const out: string[] = [];
  const lead: string[] = [];
  const d = signedDuration(r.added_sec);
  if (d) lead.push(d);
  if (Number.isFinite(r.added_m)) lead.push((r.added_m < 0 ? "−" : "+") + formatDistance(Math.abs(r.added_m) / 1000, unit, { fine: true }));
  if (lead.length) out.push(lead.join(" · "));
  if (r.estimated) out.push(WHAT_IF_ROUGH);
  const at = r.destination?.arrival_at ? new Date(r.destination.arrival_at) : null;
  if (at && !Number.isNaN(at.getTime())) {
    out.push(`About ${at.toLocaleTimeString([], { hour: "numeric", minute: "2-digit", timeZone: zone })} at the destination (estimate)`);
  }
  const drop = r.next_charge?.soc_drop_pts;
  if (drop != null && drop >= 1) out.push(`About ${Math.round(drop)} points lower at ${r.next_charge?.is_destination ? "the destination" : "the next charger"}`);
  for (const w of r.warnings ?? []) {
    if (w === "may_not_reach_next_charge") out.push("May not reach the next charger (estimate)");
    else if (w === "may_not_reach_destination") out.push("May not reach the destination (estimate)");
  }
  if (out.length) out.push(WHAT_IF_NOTICE);
  return out;
}

// A failed preview says nothing about the route: unavailable (404) shows nothing, busy (429) says so.
export function whatIfFailureLine(status: number | undefined): string | null {
  return status === 429 ? "Preview is busy. Try again in a moment." : null;
}
