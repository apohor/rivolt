// Small DOM controls for the MapLibre drive map, the same chips the Leaflet map had: the D/L basemap picker, the speed legend, the GPS
// staleness badge and the "tiles unavailable" notice. Each is a MapLibre IControl built from plain DOM, so the existing e2e selectors
// (.rivolt-flavor-control and its buttons) keep working.
import type { IControl, Map as MLMap } from "maplibre-gl";
import { MAP_CAPTION_PX } from "../lib/mapCaption";
import { MODE_LABEL, MODE_TITLE, rampCSS, type Mode } from "./metrics";
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

const MI_PER_KM = 0.6213711922;
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

// foldCredits starts the attribution control folded to its (i) and makes data-open the one state behind it (map.css hides the credits unless
// it is set). The library's own handler and the native <details> toggle both flip `open` on a tap and reopen it as sources load, so the tap
// is taken in the capture phase, kept from them, and the element is held open: what shows is data-open alone.
export function foldCredits(container: HTMLElement): void {
  const attrib = container.querySelector<HTMLDetailsElement>(".maplibregl-ctrl-attrib");
  if (!attrib) return;
  attrib.setAttribute("data-fold", "");
  attrib.open = true;
  attrib.addEventListener(
    "click",
    (e) => {
      if (!(e.target as Element).closest(".maplibregl-ctrl-attrib-button")) return;
      e.preventDefault();
      e.stopPropagation();
      attrib.toggleAttribute("data-open");
      attrib.open = true;
    },
    true,
  );
}

// modeLegend is the bottom-left card of the drive map: a Speed / Efficiency / Elevation switch (only when more than one mode qualifies) and,
// under it, the active mode's name, a gradient bar and the values at its two ends (the 5th and 95th percentile the colours are scaled to).
export type LegendState = { mode: Mode; flavor: MapFlavor; ramp: string[]; lo: string; hi: string };
export function modeLegend(
  modes: Mode[],
  onMode: (m: Mode) => void,
  initial: LegendState,
  // On a phone the D/L basemap picker lives in the card (a small segmented control), so no separate control covers a label.
  dl?: (f: MapFlavor) => void,
): IControl & { update: (s: LegendState) => void } {
  let root: HTMLElement | undefined;
  const buttons = new Map<Mode, HTMLButtonElement>();
  const dlButtons = new Map<MapFlavor, HTMLButtonElement>();
  let title: HTMLElement, lo: HTMLElement, hi: HTMLElement, bar: HTMLElement;
  const update = (s: LegendState) => {
    if (!root) return;
    const c = CHIP[s.flavor];
    root.style.background = c.bg;
    root.style.borderColor = c.border;
    root.style.color = c.text;
    root.style.boxShadow = c.shadow;
    root.setAttribute("data-mode", s.mode);
    title.textContent = MODE_TITLE[s.mode];
    title.style.color = c.muted;
    lo.textContent = s.lo;
    hi.textContent = s.hi;
    // The end values are the numbers the colours mean: full text colour, semibold; the title is the quieter line.
    lo.style.cssText = hi.style.cssText = "font-weight:600;white-space:nowrap";
    bar.style.background = rampCSS(s.ramp);
    for (const [m, b] of buttons) {
      const on = m === s.mode;
      b.setAttribute("aria-pressed", String(on));
      b.style.background = on ? "#262626" : "transparent";
      b.style.color = on ? "#fafafa" : c.muted;
    }
    for (const [f, b] of dlButtons) {
      const on = f === s.flavor;
      b.style.background = on ? "#262626" : "transparent";
      b.style.color = on ? "#fafafa" : c.muted;
    }
  };
  const c = control(() => {
    const div = document.createElement("div");
    div.className = "maplibregl-ctrl rivolt-speed-legend rivolt-legend";
    div.style.cssText =
      "border:1px solid;border-radius:8px;padding:4px 6px;display:flex;flex-direction:column;gap:3px;max-width:min(300px,72vw);" +
      `font:${MAP_CAPTION_PX}px/1.2 ui-sans-serif,system-ui;backdrop-filter:blur(2px);`;
    const top = document.createElement("div");
    top.style.cssText = "display:flex;gap:6px;align-items:center";
    if (modes.length > 1) {
      const row = document.createElement("div");
      row.setAttribute("role", "group");
      row.setAttribute("aria-label", "Route colour");
      row.style.cssText = "display:flex;gap:2px;flex:1";
      for (const m of modes) {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = MODE_LABEL[m];
        b.style.cssText = "flex:1;border:0;border-radius:5px;padding:2px 6px;font:inherit;font-weight:600;cursor:pointer;white-space:nowrap;";
        b.addEventListener("click", (e) => {
          e.stopPropagation();
          onMode(m);
        });
        row.appendChild(b);
        buttons.set(m, b);
      }
      top.appendChild(row);
    }
    title = document.createElement("div");
    title.style.cssText = "white-space:nowrap;overflow:hidden;text-overflow:ellipsis";
    const scale = document.createElement("div");
    scale.style.cssText = "display:flex;align-items:center;gap:6px";
    lo = document.createElement("span");
    hi = document.createElement("span");
    bar = document.createElement("span");
    bar.style.cssText = "flex:1;min-width:56px;height:6px;border-radius:3px";
    scale.append(lo, bar, hi);
    if (dl) {
      // Same class, titles and active look as the standalone picker, so it is found the same way.
      const grp = document.createElement("div");
      grp.className = "rivolt-flavor-control";
      grp.style.cssText = "display:inline-flex;gap:2px;margin-left:auto";
      for (const f of ["dark", "light"] as MapFlavor[]) {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = f[0].toUpperCase();
        b.title = `${f[0].toUpperCase()}${f.slice(1)} basemap`;
        b.style.cssText = "width:20px;height:20px;border:1px solid transparent;border-radius:4px;background:transparent;cursor:pointer;padding:0;font:inherit;font-weight:600;";
        b.addEventListener("click", (e) => {
          e.stopPropagation();
          dl(f);
        });
        grp.appendChild(b);
        dlButtons.set(f, b);
      }
      top.appendChild(grp);
    }
    if (top.childElementCount > 0) div.appendChild(top);
    div.append(title, scale);
    root = div;
    update(initial);
    return div;
  },
  // On a phone the one-line attribution (bottom-right) would run under the card's right end: lift the card above it.
  (el, map) => {
    if (map.getContainer().clientWidth < 520) el.style.marginBottom = "22px";
  });
  return Object.assign(c, { update });
}

// readoutElement is the pill beside the scrub dot: the time and the active mode's value there.
export function readoutElement(text: string): HTMLElement {
  const el = document.createElement("div");
  el.className = "rivolt-readout";
  el.textContent = text;
  el.style.cssText =
    "background:rgba(14,17,21,0.92);color:#F5F5F5;border:1px solid #1D2127;border-radius:9999px;padding:2px 8px;white-space:nowrap;pointer-events:none;" +
    `font:600 ${MAP_CAPTION_PX + 1}px/1.3 ui-sans-serif,system-ui;box-shadow:0 1px 2px rgba(0,0,0,0.4);`;
  return el;
}

// chipLegend is a bottom-left card of coloured dots and labels (the charges map's power classes), in the basemap's card colours.
export function chipLegend(items: { color: string; label: string }[], active: MapFlavor = "dark"): IControl & { refresh: (f: MapFlavor) => void } {
  let root: HTMLElement | undefined;
  const refresh = (f: MapFlavor) => {
    if (!root) return;
    const c = CHIP[f];
    root.style.background = c.bg;
    root.style.borderColor = c.border;
    root.style.color = c.text;
    root.style.boxShadow = c.shadow;
  };
  const c = control(() => {
    const div = document.createElement("div");
    div.className = "maplibregl-ctrl rivolt-charge-legend";
    div.style.cssText =
      "border:1px solid;border-radius:6px;padding:4px 6px;display:flex;gap:8px;flex-wrap:wrap;align-items:center;" +
      `font:${MAP_CAPTION_PX}px/1.1 ui-sans-serif,system-ui;backdrop-filter:blur(2px);`;
    for (const it of items) {
      const span = document.createElement("span");
      span.style.cssText = "display:inline-flex;align-items:center;gap:3px";
      const dot = document.createElement("span");
      dot.style.cssText = `display:inline-block;width:8px;height:8px;border-radius:9999px;background:${it.color}`;
      span.append(dot, document.createTextNode(it.label));
      div.appendChild(span);
    }
    root = div;
    refresh(active);
    return div;
  },
  // Wrap inside the map, clear of the credits' (i) at the bottom right (it is 29 px wide with a 10 px margin), and lift above it on a phone.
  (el, map) => {
    el.style.maxWidth = `${Math.max(120, map.getContainer().clientWidth - 64)}px`;
  });
  return Object.assign(c, { refresh });
}

// legLegend is the bottom-left card of a plan's map: its title and a bar split into the legs between charging stops (each its own colour and
// as long as the leg is), with the trip's start and total length at the ends. Hover a segment for that leg's length.
export type LegLegendState = { title: string; legs: { color: string; miles: number }[]; flavor: MapFlavor; km: boolean };
export function legLegend(initial: LegLegendState): IControl & { update: (s: LegLegendState) => void } {
  let root: HTMLElement | undefined;
  let title: HTMLElement, bar: HTMLElement, end: HTMLElement, start: HTMLElement;
  const dist = (mi: number, km: boolean) => (km ? `${Math.round(mi / MI_PER_KM)} km` : `${Math.round(mi)} mi`);
  const update = (s: LegLegendState) => {
    if (!root) return;
    const c = CHIP[s.flavor];
    root.style.background = c.bg;
    root.style.borderColor = c.border;
    root.style.color = c.text;
    root.style.boxShadow = c.shadow;
    title.textContent = s.title;
    title.style.color = c.muted;
    bar.replaceChildren(
      ...s.legs.map((l, i) => {
        const seg = document.createElement("span");
        seg.style.cssText = `flex:${Math.max(l.miles, 0.01)} 1 0;background:${l.color};min-width:3px`;
        seg.title = `Leg ${i + 1} · ${dist(l.miles, s.km)}`;
        return seg;
      }),
    );
    start.textContent = "0";
    end.textContent = dist(s.legs.reduce((a, l) => a + l.miles, 0), s.km);
  };
  const c = control(
    () => {
      const div = document.createElement("div");
      div.className = "maplibregl-ctrl rivolt-leg-legend";
      div.style.cssText =
        "border:1px solid;border-radius:8px;padding:4px 6px;display:flex;flex-direction:column;gap:3px;max-width:min(300px,72vw);" +
        `font:${MAP_CAPTION_PX}px/1.2 ui-sans-serif,system-ui;backdrop-filter:blur(2px);`;
      title = document.createElement("div");
      title.style.cssText = "white-space:nowrap;overflow:hidden;text-overflow:ellipsis";
      const scale = document.createElement("div");
      scale.style.cssText = "display:flex;align-items:center;gap:6px";
      start = document.createElement("span");
      end = document.createElement("span");
      start.style.cssText = end.style.cssText = "font-weight:600;white-space:nowrap";
      bar = document.createElement("span");
      bar.style.cssText = "flex:1;min-width:72px;height:6px;border-radius:3px;overflow:hidden;display:flex;gap:2px";
      scale.append(start, bar, end);
      div.append(title, scale);
      root = div;
      update(initial);
      return div;
    },
    (el, map) => {
      if (map.getContainer().clientWidth < 520) el.style.marginBottom = "22px";
    },
  );
  return Object.assign(c, { update });
}
