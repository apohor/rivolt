import type { ChargingNetwork } from "./api";

// #906: a network row of the user's own may carry where it is. That
// location only labels the user's own charges (the map pin) and offers
// its rate in the pricing card; it never prices a charge by itself and
// never leaves the user's account.

// OWN_NETWORK_RADIUS_M is how close a charge must be to a network's saved
// location to be labelled with it.
export const OWN_NETWORK_RADIUS_M = 150;

function haversineM(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const r = 6371008.8;
  const rad = Math.PI / 180;
  const dLat = (lat2 - lat1) * rad;
  const dLon = (lon2 - lon1) * rad;
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(lat1 * rad) * Math.cos(lat2 * rad) * Math.sin(dLon / 2) ** 2;
  return 2 * r * Math.asin(Math.min(1, Math.sqrt(a)));
}

// nearestOwnNetwork is the user's own network (no slug) with a saved
// location nearest to (lat, lon) within OWN_NETWORK_RADIUS_M, or null.
export function nearestOwnNetwork(
  networks: ChargingNetwork[] | undefined,
  lat: number,
  lon: number,
): ChargingNetwork | null {
  if (!networks || !Number.isFinite(lat) || !Number.isFinite(lon) || (lat === 0 && lon === 0)) return null;
  let best: ChargingNetwork | null = null;
  let bestM = Infinity;
  for (const n of networks) {
    if (n.slug || n.lat == null || n.lon == null) continue;
    const m = haversineM(lat, lon, n.lat, n.lon);
    if (m <= OWN_NETWORK_RADIUS_M && m < bestM) {
      best = n;
      bestM = m;
    }
  }
  return best;
}

// parseNetworkLocation reads "lat, lon" as typed in Settings: "" is no
// location; otherwise both numbers, in range, not 0,0 (the server's rule).
export function parseNetworkLocation(
  text: string,
): { ok: true; lat?: number; lon?: number } | { ok: false; why: string } {
  const t = text.trim();
  if (t === "") return { ok: true };
  const parts = t.split(/[,\s]+/).filter(Boolean);
  const a = Number(parts[0]);
  const b = Number(parts[1]);
  if (parts.length !== 2 || !Number.isFinite(a) || !Number.isFinite(b)) {
    return { ok: false, why: "Enter a latitude and a longitude, e.g. 40.7128, -74.0060." };
  }
  if (a < -90 || a > 90) return { ok: false, why: "Latitude must be between -90 and 90." };
  if (b < -180 || b > 180) return { ok: false, why: "Longitude must be between -180 and 180." };
  if (a === 0 && b === 0) return { ok: false, why: "That is 0, 0 (off the coast of Africa); enter the network's location." };
  return { ok: true, lat: a, lon: b };
}

// formatNetworkLocation is the Settings field's text for a saved location.
export function formatNetworkLocation(n: Pick<ChargingNetwork, "lat" | "lon">): string {
  return n.lat != null && n.lon != null ? `${n.lat}, ${n.lon}` : "";
}
