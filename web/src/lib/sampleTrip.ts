// Public sample-trip pages (/sample-trips, /sample-trips/<slug>). The server renders each page once at startup
// and puts the plan in the page as JSON (<script type="application/json" id="sample-trip">, or id="sample-trips"
// for the hub), so the page needs no API call. Fixture data only; no coordinates besides the simplified route.

export type SampleStop = { name: string; lat?: number; lon?: number; network: string; arrive_soc: number; depart_soc: number; charge_min: number; max_kw: number; cost_low?: number | null; cost_high?: number | null; price_note?: string; price_month?: string; price_url?: string };
export type SampleOvernight = { name: string; nights?: number; parked_hours: number; l2_kw: number; added_kwh: number; soc_after: number };
export type SampleDay = {
  label: string;
  depart_at: string;
  arrive_at: string;
  from: string;
  to: string;
  distance_mi: number;
  drive_min: number;
  arrival_soc: number;
  stops: SampleStop[];
  overnight: SampleOvernight | null;
};
export type SampleTotals = { distance_mi: number; drive_min: number; charge_min: number; charge_stops: number; dcfc_cost_low?: number; dcfc_cost_high?: number; priced_stops?: number; unpriced_stops?: number; price_month?: string; price_url?: string };
export type SampleTrip = {
  slug: string;
  // IANA zone of the trip's clock times (the server prints them in it).
  tz?: string;
  title: string;
  intro: string;
  demo_url: string;
  signup_url: string;
  currency: string;
  totals: SampleTotals;
  days: SampleDay[];
  route: [number, number][];
};
export type SampleTripCard = { slug: string; title: string; intro: string; blurb?: string; totals: SampleTotals };

function readJSON(id: string): unknown {
  try {
    const el = document.getElementById(id);
    return el?.textContent ? JSON.parse(el.textContent) : null;
  } catch {
    return null;
  }
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;

export function readSampleTrip(): SampleTrip | null {
  const j = readJSON("sample-trip");
  if (!isObj(j) || typeof j.slug !== "string" || typeof j.title !== "string" || !Array.isArray(j.days) || !isObj(j.totals)) return null;
  return j as unknown as SampleTrip;
}

export function readSampleTrips(): SampleTripCard[] | null {
  const j = readJSON("sample-trips");
  if (!isObj(j) || !Array.isArray(j.trips)) return null;
  return j.trips as SampleTripCard[];
}

// The route as an SVG path in a width x height box, no tiles: an equirectangular fit with the longitude
// scaled by cos(latitude) so the shape is not stretched. Null with fewer than two points.
export function routePath(route: [number, number][], width = 600, height = 360, pad = 16): { d: string; start: [number, number]; end: [number, number]; at: (lat: number, lon: number) => [number, number] } | null {
  if (!Array.isArray(route) || route.length < 2) return null;
  const lats = route.map((p) => p[0]);
  const lons = route.map((p) => p[1]);
  const lat0 = (Math.min(...lats) + Math.max(...lats)) / 2;
  const k = Math.cos((lat0 * Math.PI) / 180);
  const xs = lons.map((l) => l * k);
  const minX = Math.min(...xs);
  const minY = Math.min(...lats);
  const w = Math.max(Math.max(...xs) - minX, 1e-6);
  const h = Math.max(Math.max(...lats) - minY, 1e-6);
  const s = Math.min((width - 2 * pad) / w, (height - 2 * pad) / h);
  const ox = (width - w * s) / 2;
  const oy = (height - h * s) / 2;
  const at = (lat: number, lon: number): [number, number] => [ox + (lon * k - minX) * s, height - (oy + (lat - minY) * s)];
  const pts = route.map((p) => at(p[0], p[1]));
  const f = (n: number) => n.toFixed(1);
  return { d: pts.map((p, i) => `${i ? "L" : "M"}${f(p[0])} ${f(p[1])}`).join(""), start: pts[0], end: pts[pts.length - 1], at };
}

export const fmtDuration = (min: number): string => {
  const m = Math.round(min);
  return m >= 60 ? `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min` : `${m} min`;
};

// The two links come from the server, but only these shapes become links: the demo host (or a /demo path) and
// /signup, so a bad value can never produce an off-site or javascript: link.
export const safeDemoUrl = (u: string): string | null => (/^(https:\/\/demo\.rivolt\.dev\/|\/demo\/)/.test(u) ? u : null);
export const safeSignupUrl = (u: string): string => (/^\/signup(\/|\?|$)/.test(u) ? u : "/signup");

// "2026-10" -> "Oct 2026"; empty when the month isn't there.
export function priceMonth(ym: string | undefined): string {
  const m = ym ? /^(\d{4})-(\d{2})$/.exec(ym) : null;
  // A real month only: "2026-13" is malformed, never "Invalid Date" on the page.
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return "";
  const d = new Date(`${ym}-15T12:00:00Z`);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleDateString("en-US", { month: "short", year: "numeric", timeZone: "UTC" });
}
