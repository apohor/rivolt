// The planner map's forecast radar (design key plan_forecast_map, Pro): NOAA RRFS reflectivity for a chosen hour of the trip, drawn from the
// server's same-origin tile route (the browser never calls NOAA). Pure helpers first, then the map layer.
import type { Map as MLMap } from "maplibre-gl";
import { radarBeforeId } from "./radar";

const SOURCE = "rivolt-forecast-radar";
const LAYER = "rivolt-forecast-radar-layer";
export const FORECAST_OPACITY = 0.6;
// The server renders z 3 to 7 (a 3 km grid); above that the browser stretches the z7 tile.
export const FORECAST_MIN_ZOOM = 3;
export const FORECAST_MAX_ZOOM = 7;
// A trip scrubs at most this many hours (the forecast runs about two days).
export const FORECAST_MAX_SLOTS = 48;
// The scrubber settles this long before tiles are asked for: one hour at z7 is dozens of tiles, and the server allows 600 a minute.
export const FORECAST_DEBOUNCE_MS = 300;

// The tile route has path parameters, so its URL is built from the origin (the server contract test reads the "/api/..." literals as calls).
export const FORECAST_HOURS_URL = "/api/maps/forecast-radar/hours";
export const FORECAST_CREDIT = "NOAA RRFS";
export const LINE_PAST_HORIZON = "The forecast doesn't reach this hour.";
export const LINE_OUTSIDE_US = "The forecast covers the contiguous US.";
export const LINE_DOWN = "Forecast unavailable";

export type ForecastHour = { fh: number; valid_unix: number };
export type ForecastHours = {
  cycle: string;
  credit: string;
  bounds: [number, number, number, number]; // west, south, east, north
  hours: ForecastHour[];
  horizon_unix: number;
};

// "ok": hours loaded. "hidden": the account or server does not offer it (402 free, 401/403, 404 no forecast, 429): say nothing, show the live radar as
// before. "down": the feed is down (503/network): the forecast mode says "Forecast unavailable".
export type HoursResult = { kind: "ok"; data: ForecastHours } | { kind: "hidden" } | { kind: "down" };

export async function fetchForecastHours(signal?: AbortSignal): Promise<HoursResult> {
  try {
    const r = await fetch(FORECAST_HOURS_URL, { signal, credentials: "same-origin" });
    if (r.status === 401 || r.status === 402 || r.status === 403 || r.status === 404 || r.status === 429) return { kind: "hidden" };
    if (!r.ok) return { kind: "down" };
    const j = (await r.json()) as Partial<ForecastHours>;
    if (!j || typeof j.cycle !== "string" || !Array.isArray(j.hours) || !Array.isArray(j.bounds) || j.bounds.length !== 4) return { kind: "down" };
    return { kind: "ok", data: { cycle: j.cycle, credit: j.credit || FORECAST_CREDIT, bounds: j.bounds as ForecastHours["bounds"], hours: j.hours, horizon_unix: j.horizon_unix ?? 0 } };
  } catch {
    return { kind: "down" };
  }
}

// forecastSlots are the scrubber's steps, one per hour on the hour, from the hour the trip leaves to the hour it arrives in (at least one, at most
// FORECAST_MAX_SLOTS). Each carries the forecast hour that shows it, or null where the forecast has none (before its first hour or past its horizon).
export type Slot = { t: number; fh: number | null };
export function forecastSlots(hours: ForecastHour[], departUnix: number, arriveUnix: number): Slot[] {
  if (!(departUnix > 0)) return [];
  const first = Math.floor(departUnix / 3600) * 3600;
  const last = Math.max(first, Math.floor(arriveUnix / 3600) * 3600);
  const byTime = new Map(hours.map((h) => [h.valid_unix, h.fh]));
  const out: Slot[] = [];
  for (let t = first; t <= last && out.length < FORECAST_MAX_SLOTS; t += 3600) out.push({ t, fh: byTime.has(t) ? (byTime.get(t) as number) : null });
  return out;
}

// forecastTileURL is the tile template for one cycle and hour; both are in the path, so the browser and Cloudflare cache each hour for good.
export function forecastTileURL(origin: string, cycle: string, fh: number): string {
  return `${origin}/api/maps/forecast-radar/${encodeURIComponent(cycle)}/${fh}/{z}/{x}/{y}.png`;
}

// inBounds: the point lies inside the grid's box (the route's middle decides whether this trip is in the forecast's reach).
export function inBounds(b: [number, number, number, number], lat: number, lon: number): boolean {
  return lon >= b[0] && lat >= b[1] && lon <= b[2] && lat <= b[3];
}

// slotLine is the one plain line under the scrubber, or "" when the hour is drawn.
export function slotLine(slot: Slot | undefined, inside: boolean): string {
  if (!inside) return LINE_OUTSIDE_US;
  if (!slot || slot.fh == null) return LINE_PAST_HORIZON;
  return "";
}

// forecastLabel: "Forecast for 3 pm", in the viewer's time.
export function forecastLabel(t: number): string {
  return `Forecast for ${new Date(t * 1000).toLocaleTimeString([], { hour: "numeric" }).toLowerCase()}`;
}

export function showForecast(map: MLMap, cycle: string, fh: number): void {
  const tiles = [forecastTileURL(location.origin, cycle, fh)];
  const src = map.getSource(SOURCE) as (unknown & { setTiles?: (t: string[]) => void }) | undefined;
  if (src?.setTiles) {
    src.setTiles(tiles);
    return;
  }
  if (src) hideForecast(map);
  map.addSource(SOURCE, { type: "raster", tiles, tileSize: 256, minzoom: FORECAST_MIN_ZOOM, maxzoom: FORECAST_MAX_ZOOM, attribution: "Radar forecast: NOAA RRFS" });
  map.addLayer({ id: LAYER, type: "raster", source: SOURCE, paint: { "raster-opacity": FORECAST_OPACITY, "raster-fade-duration": 0 } }, radarBeforeId(map));
}

export function hideForecast(map: MLMap): void {
  if (!map.getStyle()) return;
  if (map.getLayer(LAYER)) map.removeLayer(LAYER);
  if (map.getSource(SOURCE)) map.removeSource(SOURCE);
}
