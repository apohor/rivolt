// The drive map's "Start 8:13 PM" / "End 11:28 PM" pills. They are map symbols (an image each, from a canvas), not DOM markers, so the basemap's
// own place labels yield to them: MapLibre places the topmost symbol layer first, and `icon-ignore-placement: false` makes it block what is
// under it. A transparent 22 px symbol at each pin reserves the pin's own room the same way. Without a 2D canvas (jsdom) there are no pills.
import type { GeoJSONSource, Map as MLMap } from "maplibre-gl";
import { MAP_CAPTION_PX } from "../lib/mapCaption";
import type { MapFlavor } from "./style";

const CARD = {
  dark: { bg: "rgba(14,17,21,0.92)", border: "#1D2127", text: "#F5F5F5" },
  light: { bg: "rgba(252,253,254,0.94)", border: "#E1E3E7", text: "#171717" },
} as const;

export const PILLS_SOURCE = "rivolt-pills";
const PILLS_LAYER = "rivolt-pills";
const RESERVE_LAYER = "rivolt-pin-reserve";
const RESERVE_IMAGE = "rivolt-pin-reserve";

export type PillImage = { width: number; height: number; data: Uint8ClampedArray; pixelRatio: number };

// pillImage draws a rounded pill with `text` on a canvas; null when there is no canvas to draw on.
export function pillImage(text: string, flavor: MapFlavor, ratio = 2): PillImage | null {
  if (typeof document === "undefined") return null;
  const canvas = document.createElement("canvas");
  const ctx = canvas.getContext?.("2d");
  if (!ctx) return null;
  const c = CARD[flavor];
  const font = `600 ${MAP_CAPTION_PX + 1}px ui-sans-serif, system-ui, sans-serif`;
  ctx.font = font;
  const textW = Math.ceil(ctx.measureText(text).width);
  const w = textW + 18;
  const h = MAP_CAPTION_PX + 12;
  canvas.width = w * ratio;
  canvas.height = h * ratio;
  ctx.scale(ratio, ratio);
  ctx.font = font;
  ctx.textBaseline = "middle";
  ctx.beginPath();
  const r = h / 2;
  if (typeof ctx.roundRect === "function") ctx.roundRect(0.5, 0.5, w - 1, h - 1, r);
  else {
    // Older Safari: the same pill from arcs.
    ctx.moveTo(r, 0.5);
    ctx.arcTo(w - 0.5, 0.5, w - 0.5, h - 0.5, r);
    ctx.arcTo(w - 0.5, h - 0.5, 0.5, h - 0.5, r);
    ctx.arcTo(0.5, h - 0.5, 0.5, 0.5, r);
    ctx.arcTo(0.5, 0.5, w - 0.5, 0.5, r);
    ctx.closePath();
  }
  ctx.fillStyle = c.bg;
  ctx.fill();
  ctx.strokeStyle = c.border;
  ctx.lineWidth = 1;
  ctx.stroke();
  ctx.fillStyle = c.text;
  ctx.fillText(text, 9, h / 2 + 0.5);
  const img = ctx.getImageData(0, 0, w * ratio, h * ratio);
  return { width: img.width, height: img.height, data: img.data, pixelRatio: ratio };
}

export type PillSpec = { id: string; text: string; lngLat: [number, number]; anchor: string; offset: [number, number] };

// setPills (re)creates the pill images and layers; safe to call after every style load. Specs with no image (no canvas) are skipped.
export function setPills(map: MLMap, specs: PillSpec[], flavor: MapFlavor): void {
  for (const id of [...specs.map((s) => `pill-${s.id}`), RESERVE_IMAGE]) if (map.hasImage(id)) map.removeImage(id);
  const features = [];
  for (const s of specs) {
    const img = pillImage(s.text, flavor, Math.min(3, Math.max(1, Math.round(window.devicePixelRatio || 2))));
    if (!img) continue;
    map.addImage(`pill-${s.id}`, { width: img.width, height: img.height, data: img.data }, { pixelRatio: img.pixelRatio });
    features.push({
      type: "Feature" as const,
      geometry: { type: "Point" as const, coordinates: s.lngLat },
      properties: { image: `pill-${s.id}`, anchor: s.anchor, offset: s.offset },
    });
  }
  if (features.length === 0) return;
  // The reserve: a clear 22 px square at each pin.
  map.addImage(RESERVE_IMAGE, { width: 22, height: 22, data: new Uint8ClampedArray(22 * 22 * 4) });
  const data = { type: "FeatureCollection" as const, features };
  const src = map.getSource(PILLS_SOURCE) as GeoJSONSource | undefined;
  if (src) src.setData(data as never);
  else map.addSource(PILLS_SOURCE, { type: "geojson", data: data as never });
  const common = { "icon-allow-overlap": true, "icon-ignore-placement": false } as const;
  if (!map.getLayer(RESERVE_LAYER)) {
    map.addLayer({ id: RESERVE_LAYER, type: "symbol", source: PILLS_SOURCE, layout: { ...common, "icon-image": RESERVE_IMAGE } });
    map.addLayer({
      id: PILLS_LAYER,
      type: "symbol",
      source: PILLS_SOURCE,
      layout: { ...common, "icon-image": ["get", "image"], "icon-anchor": ["get", "anchor"], "icon-offset": ["get", "offset"] } as never,
    });
  }
}

// placePills picks, for each pin, the side of it with more room in the map's view (so a pill never lies across the line toward the middle of the
// route), and when both pins are on one side close together, puts the start above its pin and the end below.
export function placePills(map: MLMap, start: [number, number] | null, end: [number, number] | null): { start: { anchor: string; offset: [number, number] }; end: { anchor: string; offset: [number, number] } } {
  const w = map.getContainer().clientWidth || 1;
  const side = (ll: [number, number] | null) => {
    if (!ll) return "right";
    return map.project(ll).x > w / 2 ? "right" : "left";
  };
  const a = side(start);
  const b = side(end);
  const off = (s: string): [number, number] => (s === "right" ? [-16, 0] : [16, 0]);
  const out = { start: { anchor: a, offset: off(a) }, end: { anchor: b, offset: off(b) } };
  if (start && end && a === b) {
    const ps = map.project(start);
    const pe = map.project(end);
    if (Math.abs(ps.y - pe.y) < 28 && Math.abs(ps.x - pe.x) < 120) {
      out.start = { anchor: `bottom-${a === "right" ? "right" : "left"}`, offset: [a === "right" ? -4 : 4, -10] };
      out.end = { anchor: `top-${b === "right" ? "right" : "left"}`, offset: [b === "right" ? -4 : 4, 10] };
    }
  }
  return out;
}
