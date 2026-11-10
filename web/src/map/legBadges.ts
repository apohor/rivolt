// The weather badges on a planned route: one pill at each leg's midpoint with a small icon, the temperature and what the wind does along the
// direction of travel ("64° ↗ 12 mph", "21° snow expected"). Facts only; the energy effect is not on the map. A tap opens the leg's details.
import type { Map as MLMap, Marker, Popup } from "maplibre-gl";
import type { LatLng } from "./route";
import type { MapFlavor } from "./style";
import type { Box } from "./pills";
import { legWeatherTags, type LegWeatherFacts } from "../plan/legWeather";

export type LegWeatherInfo = {
  facts: LegWeatherFacts;
  // "Union Station to Mojave"
  title: string;
  // Lines for the details popup (the estimate line, the climb line).
  note: string[];
};

const sq = (a: LatLng, b: LatLng) => (a[0] - b[0]) ** 2 + ((a[1] - b[1]) * Math.cos((a[0] * Math.PI) / 180)) ** 2;
const nearest = (path: LatLng[], p: LatLng, from = 0) => {
  let best = from;
  for (let i = from; i < path.length; i++) if (sq(path[i], p) < sq(path[best], p)) best = i;
  return best;
};

// legAnchor is the point on the drawn route half way (by vertex) between the leg's two stops, and the direction of travel there as [dLat, dLon].
// With no drawn route it is the straight midpoint.
export function legAnchor(path: LatLng[], from: LatLng, to: LatLng): { at: LatLng; dir: [number, number] } {
  const straight: LatLng = [(from[0] + to[0]) / 2, (from[1] + to[1]) / 2];
  if (path.length < 3) return { at: straight, dir: [to[0] - from[0], to[1] - from[1]] };
  const a = nearest(path, from);
  const b = nearest(path, to, a);
  if (b - a < 2) return { at: straight, dir: [to[0] - from[0], to[1] - from[1]] };
  const m = Math.floor((a + b) / 2);
  const p = path[Math.max(a, m - 2)];
  const q = path[Math.min(b, m + 2)];
  return { at: path[m], dir: [q[0] - p[0], q[1] - p[1]] };
}

// Degrees clockwise from north for a travel direction [dLat, dLon]; on the north-up plan map that is also clockwise from screen-up.
export function travelBearing(at: LatLng, dir: [number, number]): number {
  return (Math.atan2(dir[1] * Math.cos((at[0] * Math.PI) / 180), dir[0]) * 180) / Math.PI;
}

const NS = "http://www.w3.org/2000/svg";
function svg(paths: string, rotate = 0, size = 14): SVGElement {
  const s = document.createElementNS(NS, "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("width", String(size));
  s.setAttribute("height", String(size));
  s.setAttribute("fill", "none");
  s.setAttribute("stroke", "currentColor");
  s.setAttribute("stroke-width", "2");
  s.setAttribute("stroke-linecap", "round");
  s.setAttribute("stroke-linejoin", "round");
  s.setAttribute("aria-hidden", "true");
  if (rotate) s.style.transform = `rotate(${rotate}deg)`;
  s.innerHTML = paths;
  return s;
}
const ICON_SNOW = '<path d="M12 2v20M4.9 7l14.2 10M19.1 7 4.9 17"/>';
const ICON_RAIN = '<path d="M20 16.2A4.5 4.5 0 0 0 17.5 8h-1.8A7 7 0 1 0 4 14.9"/><path d="M8 19v2M12 19v2M16 19v2"/>';
const ICON_TEMP = '<path d="M14 14.8V4a2 2 0 0 0-4 0v10.8a4 4 0 1 0 4 0Z"/>';
const ICON_ARROW = '<path d="M12 19V5M5 12l7-7 7 7"/>';

// The pill's DOM. Wind: an arrow that points where the air moves relative to the way you are driving, on screen (a tailwind points with you).
export function badgeElement(f: LegWeatherFacts, travelDeg: number, label: string, flavor: MapFlavor): HTMLButtonElement {
  const b = document.createElement("button");
  b.type = "button";
  b.setAttribute("data-leg-badge", "");
  b.setAttribute("aria-label", `${label}. Leg details`);
  b.style.cssText = "display:flex;align-items:center;gap:5px;padding:3px 9px;border-radius:9999px;font:600 12px/1.2 ui-sans-serif,system-ui,sans-serif;white-space:nowrap;cursor:pointer;box-shadow:0 2px 8px rgba(0,0,0,.3);" + (flavor === "dark" ? "background:rgba(14,17,21,.92);color:#f5f5f5;border:1px solid #1d2127" : "background:rgba(252,253,254,.94);color:#171717;border:1px solid #e1e3e7");
  b.appendChild(svg(f.snow ? ICON_SNOW : f.rain ? ICON_RAIN : ICON_TEMP));
  if (f.tempShort) {
    const t = document.createElement("span");
    t.textContent = f.tempShort;
    b.appendChild(t);
  }
  if (f.snow) {
    const s = document.createElement("span");
    s.style.fontWeight = "500";
    s.textContent = "snow expected";
    b.appendChild(s);
  } else if (f.wind) {
    b.appendChild(svg(ICON_ARROW, f.wind.kind === "tail" ? travelDeg : travelDeg + 180, 12));
    const w = document.createElement("span");
    w.style.fontWeight = "500";
    w.textContent = `${f.wind.speed} ${f.wind.unit}`;
    b.appendChild(w);
  }
  return b;
}

export const badgeLabel = (info: LegWeatherInfo) => `${info.title}: ${legWeatherTags(info.facts).join(", ")}`;

// drawLegBadges puts one badge at each leg's midpoint, nudged to the right of the line so it does not sit on it, and returns a remover. A badge
// whose box would lie over one already placed, or over a pin or a stop's pill (`avoid`), or past the map's edge, goes to the other side of the line, and
// if neither side is clear it is left out (the nearer legs win), so a crowded route shows fewer rather than a pile.
export function drawLegBadges(
  ml: { Marker: new (o: { element: HTMLElement; anchor: "left" | "right"; offset: [number, number] }) => Marker },
  map: MLMap,
  path: LatLng[],
  stops: LatLng[],
  infos: (LegWeatherInfo | null)[],
  flavor: MapFlavor,
  onOpen: (info: LegWeatherInfo, at: LatLng, el: HTMLElement) => void,
  avoid: Box[] = [],
): () => void {
  const markers: Marker[] = [];
  const taken: { x0: number; y0: number; x1: number; y1: number }[] = [];
  infos.forEach((info, i) => {
    if (!info || !stops[i] || !stops[i + 1]) return;
    const { at, dir } = legAnchor(path, stops[i], stops[i + 1]);
    const deg = travelBearing(at, dir);
    const el = badgeElement(info.facts, deg, badgeLabel(info), flavor);
    // To the right of the direction of travel: a left-anchored pill when the line heads up the screen, a right-anchored one when it heads down.
    const p = map.project([at[1], at[0]]);
    const w = 150;
    // ...unless that side has no room: then the other.
    const width = map.getContainer().clientWidth;
    const roomRight = width - p.x - 12;
    const preferRight = roomRight < w && p.x - 12 >= w ? false : roomRight >= w ? Math.sin((deg * Math.PI) / 180) >= 0 || p.x - 12 < w : true;
    const clear = (right: boolean) => {
      const full = right ? { x0: p.x + 12, x1: p.x + 12 + w, y0: p.y - 12, y1: p.y + 12 } : { x0: p.x - 12 - w, x1: p.x - 12, y0: p.y - 12, y1: p.y + 12 };
      const inside = full.x0 >= 4 && full.x1 <= width - 4;
      return inside && ![...taken, ...avoid].some((t) => t.x0 < full.x1 && t.x1 > full.x0 && t.y0 < full.y1 && t.y1 > full.y0) ? full : null;
    };
    // The side of the line it prefers, else the other one.
    const right = clear(preferRight) ? preferRight : !preferRight;
    const full = clear(right);
    if (!full) return;
    const anchor = right ? "left" : "right";
    const offset: [number, number] = [right ? 12 : -12, 0];
    taken.push(full);
    el.addEventListener("click", (e) => {
      e.stopPropagation();
      onOpen(info, at, el);
    });
    markers.push(new ml.Marker({ element: el, anchor, offset }).setLngLat([at[1], at[0]]).addTo(map));
  });
  return () => markers.forEach((m) => m.remove());
}

export type { Popup };
