// The live weather radar layer: NOAA/NWS base reflectivity, drawn from the server's same-origin tile proxy (browsers never call NOAA).
import type { Map as MLMap } from "maplibre-gl";
import { tilesRadar } from "../lib/config";
import { inDemo } from "../lib/demo";
import { firstLabelLayer } from "./route";

const SOURCE = "rivolt-radar";
const LAYER = "rivolt-radar-layer";
// RADAR_OPACITY keeps the basemap readable under the echoes.
export const RADAR_OPACITY = 0.6;
// RADAR_REFRESH_MS: the feed updates about every 2 minutes; 3 minutes is well inside the 2-5 minute ask and the server caches per frame.
export const RADAR_REFRESH_MS = 3 * 60 * 1000;
// Layers the radar must stay under: the route line and its glow/casing.
const ROUTE_LAYER_PREFIXES = ["rivolt-track", "rivolt-drive", "rivolt-route"];

export type RadarFrame = { frameUnix: number };

// radarEnabled is whether the server advertises the radar (the button is hidden otherwise). Never in the signed-out demo: the server does not serve
// it radar, and a button that can only say "Radar unavailable" would read as a fault.
export function radarEnabled(): boolean {
  return tilesRadar().url !== "" && !inDemo();
}

// RADAR_DENIED is what fetchRadarFrame returns when the server will not serve this visitor radar (401/403): not a fault, so nothing is said.
export const RADAR_DENIED = "denied";

// fetchRadarFrame asks the server for the newest frame time; null when the feed is down or stale (503) or the call fails, RADAR_DENIED on 401/403.
export async function fetchRadarFrame(signal?: AbortSignal): Promise<RadarFrame | typeof RADAR_DENIED | null> {
  const t = tilesRadar();
  if (!t.frameUrl) return null;
  try {
    const r = await fetch(t.frameUrl, { signal, credentials: "same-origin" });
    if (r.status === 401 || r.status === 403) return RADAR_DENIED;
    if (!r.ok) return null;
    const j = (await r.json()) as { frame_unix?: number };
    return typeof j.frame_unix === "number" && j.frame_unix > 0 ? { frameUnix: j.frame_unix } : null;
  } catch {
    return null;
  }
}

// radarBeforeId is the layer the radar goes under: the lowest route layer, else the first label, so the route and the names stay on top.
export function radarBeforeId(map: MLMap): string | undefined {
  const layers = map.getStyle()?.layers ?? [];
  const route = layers.find((l) => ROUTE_LAYER_PREFIXES.some((p) => l.id.startsWith(p)));
  return route?.id ?? firstLabelLayer(map);
}

// radarTiles is the tile template for one frame; the frame time in the URL keys both the browser cache and the server's.
export function radarTiles(origin: string, frameUnix: number): string {
  return `${origin}${tilesRadar().url}?t=${frameUnix}`;
}

// showRadar adds the layer, or points it at a newer frame.
export function showRadar(map: MLMap, frameUnix: number): void {
  const t = tilesRadar();
  if (!t.url) return;
  const tiles = [radarTiles(location.origin, frameUnix)];
  const src = map.getSource(SOURCE) as (unknown & { setTiles?: (t: string[]) => void }) | undefined;
  if (src?.setTiles) {
    src.setTiles(tiles);
    return;
  }
  if (src) {
    if (map.getLayer(LAYER)) map.removeLayer(LAYER);
    map.removeSource(SOURCE);
  }
  map.addSource(SOURCE, { type: "raster", tiles, tileSize: 256, minzoom: t.minZoom, maxzoom: t.maxZoom, attribution: "Radar: NOAA/NWS" });
  map.addLayer({ id: LAYER, type: "raster", source: SOURCE, paint: { "raster-opacity": RADAR_OPACITY, "raster-fade-duration": 0 } }, radarBeforeId(map));
}

// hideRadar removes the layer and its source.
export function hideRadar(map: MLMap): void {
  if (!map.getStyle()) return;
  if (map.getLayer(LAYER)) map.removeLayer(LAYER);
  if (map.getSource(SOURCE)) map.removeSource(SOURCE);
}

// radarTime is the time of the picture ("6:42 PM") in the viewer's local time.
export function radarTime(frameUnix: number): string {
  return new Date(frameUnix * 1000).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

// radarAsOf is the end of the radar strip, the credit and the time of the picture ("NOAA/NWS · 6:42 PM"), as on the iPhone; short, so the strip stays
// on one line on a phone.
export function radarAsOf(frameUnix: number): string {
  return `NOAA/NWS · ${radarTime(frameUnix)}`;
}

// radarSpoken is what a screen reader says for the strip: "light, moderate, heavy, NOAA/NWS radar, 6:42 PM".
export function radarSpoken(frameUnix: number): string {
  return `light, moderate, heavy, NOAA/NWS radar, ${radarTime(frameUnix)}`;
}
