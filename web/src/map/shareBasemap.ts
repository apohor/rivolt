// The basemap under the share card's route (design_preview "share_map"): the area the drive went through, drawn by MapLibre off screen from our own
// tiles and copied into a canvas the card painter lays under the line. The style is the app's own dark basemap with no symbol layer (so no labels at
// any zoom), no glyphs, no sprite, no terrain and no radar: the only requests are same-origin /api/maps/tiles reads. The route is the server's trimmed
// postcard route (both ends and the saved home already cut off), so the map shows no more than the line did. The map is framed to the segments with at
// least 10% padding and never past zoom 12, carries no marker, pin, scale or compass, and the result is pixels only: no coordinates leave this module
// except as the line's pixel positions in the picture.
import type { LngLatBoundsLike, StyleSpecification } from "maplibre-gl";
import { tilesPMTilesURL } from "../lib/config";
import { decodePolyline } from "../lib/polyline";
import { loadMapLibre } from "./loader";
import { buildStyle } from "./style";
import { webglAvailable } from "./webgl";

export type Basemap = {
  // The map picture, drawn at twice the CSS size so the card (1080 px) stays sharp.
  image: HTMLCanvasElement;
  // The segments projected into the picture's CSS pixels (w x h), each kept apart so a cut-off stretch is never joined by a line.
  lines: [number, number][][];
  w: number;
  h: number;
};

export const BASEMAP_MAX_ZOOM = 12;
const PAD = 0.1;
const SCALE = 2;
const IDLE_TIMEOUT_MS = 15_000;

// shareBasemapStyle is the card's map style: dark whatever the app's theme (the card is a fixed dark image), labels off.
export function shareBasemapStyle(origin: string, tilesPath: string): StyleSpecification {
  return buildStyle({ flavor: "dark", origin, tilesPath, glyphsPath: "", dem: null });
}

// renderShareBasemap draws the basemap for the segments into a w x h box, or null when it cannot (no tile archive, no WebGL, a tile read that
// failed, aborted): the card then carries the plain line.
export async function renderShareBasemap(segments: string[], w: number, h: number, signal?: AbortSignal): Promise<Basemap | null> {
  const tiles = tilesPMTilesURL();
  if (!tiles.startsWith("/") || !webglAvailable()) return null;
  const paths = segments.map((s) => decodePolyline(s, 6)).filter((p) => p.length >= 2);
  if (paths.length === 0) return null;
  let minLat = Infinity;
  let maxLat = -Infinity;
  let minLon = Infinity;
  let maxLon = -Infinity;
  for (const p of paths) for (const [lat, lon] of p) {
    minLat = Math.min(minLat, lat);
    maxLat = Math.max(maxLat, lat);
    minLon = Math.min(minLon, lon);
    maxLon = Math.max(maxLon, lon);
  }
  const ml = await loadMapLibre();
  if (signal?.aborted) return null;
  const el = document.createElement("div");
  el.setAttribute("aria-hidden", "true");
  el.style.cssText = `position:fixed;left:-20000px;top:0;width:${w}px;height:${h}px;pointer-events:none`;
  document.body.appendChild(el);
  const map = new ml.Map({
    container: el,
    style: shareBasemapStyle(location.origin, tiles),
    bounds: [[minLon, minLat], [maxLon, maxLat]] as LngLatBoundsLike,
    fitBoundsOptions: { padding: { top: h * PAD, bottom: h * PAD, left: w * PAD, right: w * PAD }, maxZoom: BASEMAP_MAX_ZOOM },
    interactive: false,
    attributionControl: false,
    fadeDuration: 0,
    pixelRatio: SCALE,
    canvasContextAttributes: { preserveDrawingBuffer: true },
  });
  let failed = false;
  map.on("error", (e) => {
    const status = (e as unknown as { error?: { status?: number } }).error?.status;
    // A tile the archive does not hold (404) is empty land or sea; anything else is a read that failed.
    if (status !== 404) failed = true;
  });
  try {
    await new Promise<void>((resolve, reject) => {
      const t = window.setTimeout(() => reject(new Error("timeout")), IDLE_TIMEOUT_MS);
      const onAbort = () => reject(new Error("aborted"));
      signal?.addEventListener("abort", onAbort, { once: true });
      map.once("idle", () => {
        window.clearTimeout(t);
        signal?.removeEventListener("abort", onAbort);
        resolve();
      });
    });
    if (failed) return null;
    const image = document.createElement("canvas");
    const src = map.getCanvas();
    image.width = src.width;
    image.height = src.height;
    const g = image.getContext("2d");
    if (!g) return null;
    g.drawImage(src, 0, 0);
    const lines = paths.map((p) =>
      p.map(([lat, lon]) => {
        const pt = map.project([lon, lat]);
        return [pt.x, pt.y] as [number, number];
      }),
    );
    return { image, lines, w, h };
  } catch {
    return null;
  } finally {
    map.remove();
    el.remove();
  }
}
