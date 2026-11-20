import { decodePolyline } from "./polyline";
import { displayTimeZone } from "./format";
import type { DistanceUnit } from "./preferences";

// The live destination (design_preview "live_destination"): where the truck's own navigation is taking it, from
// GET /api/live-destination/{vehicleID}. It is drawn and shown while it is on screen and then gone: nothing is stored, logged or shared.
// miles_left is always in miles (omitted when the car does not know it); route is a precision-6 polyline.
export type LiveDestination = { name: string; lat: number; lon: number; eta: string; miles_left?: number; route?: string; route_precision?: number };

const KM_PER_MI = 1.609344;

// destinationRoute is the planned route as [lat, lon] pairs ([] when absent or malformed).
export function destinationRoute(d: LiveDestination): [number, number][] {
  return decodePolyline(d.route, d.route_precision === 5 ? 5 : 6);
}

// destinationLine is the row on the Live panel: "To Work: 12 mi left, arrive 5:40 PM" (km for metric, no distance when unknown).
export function destinationLine(d: LiveDestination, unit: DistanceUnit): string {
  const eta = new Date(d.eta);
  const arrive = Number.isFinite(eta.getTime()) ? eta.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() }) : "";
  const left = typeof d.miles_left === "number" && Number.isFinite(d.miles_left) && d.miles_left >= 0 ? distanceLeft(d.miles_left, unit) : "";
  const tail = [left ? `${left} left` : "", arrive ? `arrive ${arrive}` : ""].filter(Boolean).join(", ");
  return tail ? `To ${d.name}: ${tail}` : `To ${d.name}`;
}

function distanceLeft(miles: number, unit: DistanceUnit): string {
  const v = unit === "km" ? miles * KM_PER_MI : miles;
  return `${v < 10 ? v.toFixed(1) : String(Math.round(v))} ${unit}`;
}

// destinationShort is the one line on the Now card's drive graph: "12 mi to Work · arrive 5:40 PM" (no distance when unknown).
export function destinationShort(d: LiveDestination, unit: DistanceUnit): string {
  const eta = new Date(d.eta);
  const arrive = Number.isFinite(eta.getTime()) ? eta.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() }) : "";
  const left = typeof d.miles_left === "number" && Number.isFinite(d.miles_left) && d.miles_left >= 0 ? distanceLeft(d.miles_left, unit) : "";
  return [left ? `${left} to ${d.name}` : `To ${d.name}`, arrive ? `arrive ${arrive}` : ""].filter(Boolean).join(" · ");
}
