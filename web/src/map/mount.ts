// The shared start of every MapLibre map that is not the drive or trip map: the map on the style for the advertised tiles, the controls every
// map carries (zoom, folded credits, the tiles-unavailable note), the wheel that waits for a click, 404s swallowed, the app theme followed
// live, and a resize observer. The caller adds its layers in onStyle, which runs after every style load (a theme change drops them).
import type { Map as MLMap } from "maplibre-gl";
import { tilesPMTilesURL } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { styleFor } from "./base";
import { foldCredits, tilesNotice } from "./controls";
import type { MapLibreModule } from "./loader";
import { BASEMAP_ATTRIBUTION, type MapFlavor } from "./style";
import { TILES_UNAVAILABLE_TEXT } from "./Unavailable";

export type Mounted = {
  map: MLMap;
  // onStyle runs fn now if the style is ready and after every later style load, with the flavour in force.
  onStyle: (fn: (flavor: MapFlavor) => void) => void;
  styleReady: () => boolean;
  dispose: () => void;
};

export function mountBaseMap(ml: MapLibreModule, el: HTMLElement, init: { center: [number, number]; zoom: number }): Mounted {
  let flavor: MapFlavor = appMapFlavor();
  const map = new ml.Map({
    container: el,
    style: styleFor(flavor),
    center: init.center,
    zoom: init.zoom,
    attributionControl: false,
    dragRotate: false,
    pitchWithRotate: false,
    touchPitch: false,
    fadeDuration: 150,
  });
  el.setAttribute("data-flavor", flavor);
  map.touchZoomRotate.disableRotation();
  // Click to enable the wheel; leaving the map disables it again, so the page keeps scrolling over the map.
  map.scrollZoom.disable();
  map.on("click", () => map.scrollZoom.enable());
  map.on("mouseout", () => map.scrollZoom.disable());
  // A terrain or label tile the server does not hold is a 404: the map draws without it, quietly.
  map.on("error", (e) => {
    const err = (e as unknown as { error?: { status?: number; message?: string } }).error;
    if (err?.status !== 404) console.warn("map:", err?.message ?? "error");
  });
  map.addControl(new ml.NavigationControl({ showCompass: false }), "top-left");
  map.addControl(new ml.AttributionControl({ compact: true, customAttribution: BASEMAP_ATTRIBUTION }), "bottom-right");
  foldCredits(el);
  if (!tilesPMTilesURL().startsWith("/")) map.addControl(tilesNotice(TILES_UNAVAILABLE_TEXT), "bottom-left");

  // Not map.isStyleLoaded(): that is false while any source is loading a tile.
  let ready = false;
  const handlers: ((f: MapFlavor) => void)[] = [];
  map.on("style.load", () => {
    ready = true;
    for (const h of handlers) h(flavor);
  });
  const offTheme = onAppMapFlavor((f) => {
    if (f === flavor) return;
    flavor = f;
    ready = false;
    el.setAttribute("data-flavor", f);
    map.setStyle(styleFor(f));
  });
  const ro = new ResizeObserver(() => map.resize());
  ro.observe(el);

  return {
    map,
    onStyle(fn) {
      handlers.push(fn);
      if (ready) fn(flavor);
    },
    styleReady: () => ready,
    dispose() {
      offTheme();
      ro.disconnect();
      map.remove();
    },
  };
}
