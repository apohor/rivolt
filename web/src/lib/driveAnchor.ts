// The drive map's start and end pins come from the parked samples just before and after the drive (more reliable than the stored endpoints, which
// are whenever the first and last mid-drive packet arrived). But a parked sample is only a good anchor if it is where the drive's route is: a
// parked frame that carries a stale or default position (the truck's home, say) pins the end miles from the route and, when it lands on the start's
// own spot, makes the map think the drive began and ended in one place and draw a single pin. So an anchor farther than MAX_ANCHOR_KM from the
// route's own end is not used.
export const MAX_ANCHOR_KM = 2;

type LL = { lat: number; lon: number };

export function distanceKm(a: LL, b: LL): number {
  const R = 6371;
  const rad = Math.PI / 180;
  const dLa = (b.lat - a.lat) * rad;
  const dLo = (b.lon - a.lon) * rad;
  const h = Math.sin(dLa / 2) ** 2 + Math.cos(a.lat * rad) * Math.cos(b.lat * rad) * Math.sin(dLo / 2) ** 2;
  return 2 * R * Math.asin(Math.min(1, Math.sqrt(h)));
}

// pickAnchor is the parked-sample anchor when it sits within MAX_ANCHOR_KM of the route's own end (`routeEnd`, the first or last sample of the
// drive's path); otherwise the stored endpoint (`stored`). With no route end to compare, the anchor stands.
export function pickAnchor(anchor: LL | undefined, routeEnd: LL | undefined, stored: LL): LL {
  if (!anchor) return stored;
  if (!routeEnd) return anchor;
  return distanceKm(anchor, routeEnd) <= MAX_ANCHOR_KM ? anchor : stored;
}
