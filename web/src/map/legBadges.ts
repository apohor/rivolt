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
// whose box would lie over one already placed is left out (the nearer legs win), so a crowded route shows fewer rather than a pile. A badge goes to the
// side of the line that is clear of the pins and the overlays (`avoid`: the chips, the radar strip, the summary card), nudged sideways to stay on the map.
// Drawn again while the map moves, it returns `prev` itself when every badge would sit where it already is: a badge that is replaced under a finger
// between touch-down and touch-up loses the tap.
export type BadgesHandle = (() => void) & { key?: string };
export function drawLegBadges(
  ml: { Marker: new (o: { element: HTMLElement; anchor: "left" | "right"; offset: [number, number] }) => Marker },
  map: MLMap,
  path: LatLng[],
  stops: LatLng[],
  infos: (LegWeatherInfo | null)[],
  flavor: MapFlavor,
  onOpen: (info: LegWeatherInfo, at: LatLng, el: HTMLElement) => void,
  avoid: Box[] = [],
  pins: Box[] = [],
  prev?: BadgesHandle,
): BadgesHandle {
  const markers: Marker[] = [];
  const commits: (() => void)[] = [];
  let key = `${flavor};`;
  const taken: { x0: number; y0: number; x1: number; y1: number }[] = [];
  infos.forEach((info, i) => {
    if (!info || !stops[i] || !stops[i + 1]) return;
    const { at, dir } = legAnchor(path, stops[i], stops[i + 1]);
    const deg = travelBearing(at, dir);
    const el = badgeElement(info.facts, deg, badgeLabel(info), flavor);
    // To the right of the direction of travel: a left-anchored pill when the line heads up the screen, a right-anchored one when it heads down,
    // unless that side has no room.
    const p = map.project([at[1], at[0]]);
    const width = map.getContainer().clientWidth;
    const roomRight = width - p.x - 12;
    // About as wide as its words until it is on the map and can be measured.
    const guess = Math.min(170, 30 + 7 * (el.textContent?.length ?? 10));
    const preferRight = roomRight < guess && p.x - 12 >= guess ? false : roomRight >= guess ? Math.sin((deg * Math.PI) / 180) >= 0 || p.x - 12 < guess : true;
    // Each side of the line in turn (the preferred one first): put the badge there, measure where it really is, and nudge it sideways into the map.
    // The first side that is clear of the pins and the other overlays wins; else one that at least clears the pins and the other badges (the nearer legs win), else none.
    // On a map this narrow (a 320 px phone) there is no room to keep clear of everything: a badge over a pin still beats no badge.
    const tight = width < 340;
    type Try = { right: boolean; dx: number; box: Box; clear: boolean; free: boolean };
    const tries: Try[] = [];
    for (const right of [preferRight, !preferRight]) {
      const marker = new ml.Marker({ element: el, anchor: right ? "left" : "right", offset: [right ? 12 : -12, 0] }).setLngLat([at[1], at[0]]).addTo(map);
      // Where it really is on the map, measured (a fake map in a test has no layout: then from its point and a guessed size).
      const r = el.getBoundingClientRect?.();
      const c = map.getContainer().getBoundingClientRect?.();
      const raw = r && c && r.width > 0
        ? { x0: r.left - c.left, x1: r.right - c.left, y0: r.top - c.top, y1: r.bottom - c.top }
        : right ? { x0: p.x + 12, x1: p.x + 12 + guess, y0: p.y - 12, y1: p.y + 12 } : { x0: p.x - 12 - guess, x1: p.x - 12, y0: p.y - 12, y1: p.y + 12 };
      marker.remove();
      const dx = raw.x0 < 4 ? 4 - raw.x0 : raw.x1 > width - 4 ? width - 4 - raw.x1 : 0;
      const box = { ...raw, x0: raw.x0 + dx, x1: raw.x1 + dx };
      const hits = (list: Box[]) => list.some((t) => t.x0 < box.x1 && t.x1 > box.x0 && t.y0 < box.y1 && t.y1 > box.y0);
      // A badge wider than the map cannot be nudged in: it is not "clear".
      tries.push({ right, dx, box, free: !hits(taken) && (tight || !hits(pins)), clear: box.x0 >= 4 && box.x1 <= width - 4 && !hits(taken) && !hits(pins) && !hits(avoid) });
    }
    const pick = tries.find((t) => t.clear) ?? tries.find((t) => t.free);
    if (!pick) return;
    taken.push(pick.box);
    key += `${i}:${pick.right ? "r" : "l"}${Math.round(pick.dx)};`;
    commits.push(() => {
      const placed = new ml.Marker({ element: el, anchor: pick.right ? "left" : "right", offset: [(pick.right ? 12 : -12) + pick.dx, 0] }).setLngLat([at[1], at[0]]).addTo(map);
      el.addEventListener("click", (e) => {
        e.stopPropagation();
        onOpen(info, at, el);
      });
      markers.push(placed);
    });
  });
  if (prev && prev.key === key) return prev;
  commits.forEach((c) => c());
  const handle: BadgesHandle = () => markers.forEach((m) => m.remove());
  handle.key = key;
  return handle;
}

export type { Popup };
