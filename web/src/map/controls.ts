// Small DOM controls for the MapLibre drive map, the same chips the Leaflet map had: the D/L basemap picker, the speed legend, the GPS
// staleness badge and the "tiles unavailable" notice. Each is a MapLibre IControl built from plain DOM, so the existing e2e selectors
// (.rivolt-flavor-control and its buttons) keep working.
import type { IControl, Map as MLMap } from "maplibre-gl";
import { MAP_CAPTION_PX } from "../lib/mapCaption";
import { usedSpeedBuckets } from "../lib/speedBuckets";
import type { MapFlavor } from "./style";

function control(make: (map: MLMap) => HTMLElement, after?: (el: HTMLElement, map: MLMap) => void): IControl & { el?: HTMLElement } {
  const c: IControl & { el?: HTMLElement } = {
    onAdd(map) {
      const el = make(map);
      after?.(el, map);
      c.el = el;
      return el;
    },
    onRemove() {
      c.el?.remove();
      c.el = undefined;
    },
  };
  return c;
}

const FLAVORS: MapFlavor[] = ["dark", "light"];

// The chips follow the basemap: the card, border and text pairs from ios/design/tokens.json (roles card, border, textBody, textSecondary).
const CHIP: Record<MapFlavor, { bg: string; border: string; text: string; muted: string; shadow: string; swatchEdge: string }> = {
  dark: { bg: "rgba(14,17,21,0.85)", border: "#1D2127", text: "#D4D4D4", muted: "#A1A1A1", shadow: "0 1px 2px rgba(0,0,0,0.4)", swatchEdge: "transparent" },
  light: { bg: "rgba(252,253,254,0.92)", border: "#E1E3E7", text: "#404040", muted: "#525252", shadow: "0 1px 2px rgba(0,0,0,0.12)", swatchEdge: "rgba(0,0,0,0.3)" },
};

// flavorControl is a two-button picker. refresh(f) re-highlights the active flavor; pick(f) is called on a click.
export function flavorControl(active: MapFlavor, pick: (f: MapFlavor) => void): IControl & { refresh: (f: MapFlavor) => void } {
  const buttons = {} as Record<MapFlavor, HTMLButtonElement>;
  const refresh = (cur: MapFlavor) => {
    for (const f of FLAVORS) {
      const b = buttons[f];
      if (!b) continue;
      if (f === cur) {
        b.style.background = "#262626";
        b.style.color = "#fafafa";
        b.style.borderColor = "#404040";
      } else {
        b.style.background = "transparent";
        b.style.color = CHIP[cur].muted;
        b.style.borderColor = "transparent";
      }
    }
    const div = root;
    if (div) {
      div.style.background = CHIP[cur].bg;
      div.style.borderColor = CHIP[cur].border;
      div.style.boxShadow = CHIP[cur].shadow;
    }
  };
  let root: HTMLElement | undefined;
  const c = control((): HTMLElement => {
    const div = document.createElement("div");
    div.className = "maplibregl-ctrl rivolt-flavor-control";
    root = div;
    div.style.cssText =
      "background:rgba(10,10,10,0.78);border:1px solid #262626;border-radius:6px;padding:2px;display:inline-flex;gap:2px;" +
      `font:${MAP_CAPTION_PX}px/1 ui-sans-serif,system-ui;backdrop-filter:blur(2px);box-shadow:0 1px 2px rgba(0,0,0,0.4);`;
    for (const f of FLAVORS) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = f[0].toUpperCase();
      btn.title = `${f[0].toUpperCase()}${f.slice(1)} basemap`;
      btn.style.cssText = "width:20px;height:20px;border:1px solid transparent;border-radius:4px;background:transparent;color:#a3a3a3;cursor:pointer;padding:0;font:inherit;font-weight:600;";
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        pick(f);
      });
      div.appendChild(btn);
      buttons[f] = btn;
    }
    refresh(active);
    return div;
  });
  return Object.assign(c, { refresh });
}

// speedLegend is a small bottom-left chip naming the speed bands a drive uses ("mph  <5  25  50  65+"), the full range on hover.
export function speedLegend(speeds: (number | undefined)[], active: MapFlavor = "dark"): IControl & { refresh: (f: MapFlavor) => void } {
  const bands = usedSpeedBuckets(speeds);
  let root: HTMLElement | undefined;
  const swatches: HTMLElement[] = [];
  const units: HTMLElement[] = [];
  const refresh = (f: MapFlavor) => {
    if (!root) return;
    const c = CHIP[f];
    root.style.background = c.bg;
    root.style.borderColor = c.border;
    root.style.color = c.text;
    for (const u of units) u.style.color = c.muted;
    for (const sw of swatches) sw.style.boxShadow = c.swatchEdge === "transparent" ? "none" : `0 0 0 1px ${c.swatchEdge}`;
  };
  const c = control(
    () => {
      const div = document.createElement("div");
      div.className = "maplibregl-ctrl rivolt-speed-legend";
      div.style.cssText =
        "border:1px solid;border-radius:5px;padding:1px 4px;display:flex;gap:4px;align-items:center;" +
        `font:${MAP_CAPTION_PX}px/1.1 ui-sans-serif,system-ui;backdrop-filter:blur(2px);opacity:0.9;pointer-events:none;`;
      root = div;
      const unit = document.createElement("span");
      unit.textContent = "mph";
      units.push(unit);
      div.appendChild(unit);
      for (const b of bands) {
        const item = document.createElement("span");
        item.title = b.range;
        item.style.cssText = "display:inline-flex;align-items:center;gap:2px";
        const sw = document.createElement("span");
        sw.style.cssText = `display:inline-block;width:6px;height:3px;background:${b.color};border-radius:2px`;
        swatches.push(sw);
        item.append(sw, document.createTextNode(b.label));
        div.appendChild(item);
      }
      refresh(active);
      return div;
    },
    // On a phone the one-line attribution (bottom-right) would run under the legend's right end: lift the legend above it.
    (el, map) => {
      if (map.getContainer().clientWidth < 520) el.style.marginBottom = "22px";
    },
  );
  return Object.assign(c, { refresh });
}

// stalenessBadge is the amber top-right pill: the GPS fix behind the route is older than wall-clock would suggest.
export function stalenessBadge(fixAgeSeconds: number): IControl {
  return control(() => {
    const ageMin = Math.round(fixAgeSeconds / 60);
    const label = ageMin >= 60 ? `${Math.floor(ageMin / 60)}h ${ageMin % 60}m` : `${ageMin} min`;
    const div = document.createElement("div");
    div.className = "maplibregl-ctrl rivolt-staleness-badge";
    div.style.cssText =
      "background:rgba(120,53,15,0.85);border:1px solid #b45309;border-radius:6px;padding:3px 7px;display:inline-flex;gap:4px;align-items:center;" +
      `font:${MAP_CAPTION_PX}px/1.1 ui-sans-serif,system-ui;color:#fde68a;backdrop-filter:blur(2px);box-shadow:0 1px 2px rgba(0,0,0,0.4);`;
    div.title = `The GNSS module reported this fix ${label} before the wall-clock timestamp. The marker may be stale.`;
    const warn = document.createElement("span");
    warn.style.color = "#fbbf24";
    warn.textContent = "⚠";
    const text = document.createElement("span");
    text.textContent = `GPS fix ${label} stale`;
    div.append(warn, text);
    return div;
  });
}

// tilesNotice is the bottom-left note on a server with no tile archive: the route still draws, on a plain background.
export function tilesNotice(text: string): IControl {
  return control(() => {
    const div = document.createElement("div");
    div.className = "maplibregl-ctrl rivolt-map-notice";
    div.setAttribute("role", "note");
    div.textContent = text;
    div.style.cssText = "background:rgba(23,23,23,.9);color:#d4d4d4;border:1px solid #404040;border-radius:6px;padding:4px 8px;font-size:11px;";
    return div;
  });
}

// pinElement is a start/end/charge dot: a coloured disc with a thin dark ring, legible on both the line and the basemap.
export function pinElement(color: string, title: string): HTMLElement {
  const el = document.createElement("span");
  el.className = "rivolt-map-marker";
  el.title = title;
  el.style.cssText = `display:block;width:14px;height:14px;border-radius:9999px;background:${color};border:2px solid #0a0a0a;box-shadow:0 0 0 2px ${color}33;`;
  return el;
}

// cursorElement is the accent dot that follows the chart scrub.
export function cursorElement(): HTMLElement {
  const el = document.createElement("span");
  el.className = "rivolt-map-cursor";
  el.style.cssText = "display:block;width:12px;height:12px;border-radius:9999px;background:#fbbf24;border:2px solid #0a0a0a;pointer-events:none;";
  return el;
}
