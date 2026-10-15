import L from "leaflet";

// When no basemap archive is configured there is nothing to draw tiles
// from, and a third-party raster host isn't a safe stand-in (CARTO's
// free tiles now need a key and render an "API KEY REQUIRED" image). The
// route still draws, on a plain dark background, with this notice.
export const TILES_UNAVAILABLE_TEXT = "Map tiles unavailable on this server";

export function addTilesUnavailableNotice(map: L.Map): void {
  const el = map.getContainer();
  el.classList.add("rivolt-map-no-tiles");
  el.style.background = "#0a0a0a";
  const Notice = L.Control.extend({
    onAdd() {
      const div = L.DomUtil.create("div", "rivolt-map-notice");
      div.setAttribute("role", "note");
      div.textContent = TILES_UNAVAILABLE_TEXT;
      div.style.cssText =
        "background:rgba(23,23,23,.9);color:#d4d4d4;border:1px solid #404040;border-radius:6px;padding:4px 8px;font-size:11px;";
      L.DomEvent.disableClickPropagation(div);
      return div;
    },
  });
  new Notice({ position: "bottomleft" }).addTo(map);
}
