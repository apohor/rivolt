import { describe, expect, it } from "vitest";
import L from "leaflet";
import { addTilesUnavailableNotice, OSM_ATTRIBUTION, TILES_UNAVAILABLE_TEXT } from "./mapNotice";

describe("addTilesUnavailableNotice", () => {
  it("shows the notice on a plain background and adds no tile layer", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);
    const map = L.map(el).setView([40, -100], 5);
    addTilesUnavailableNotice(map);
    expect(el.textContent).toContain(TILES_UNAVAILABLE_TEXT);
    expect(el.classList.contains("rivolt-map-no-tiles")).toBe(true);
    let tileLayers = 0;
    map.eachLayer((l) => {
      if (l instanceof L.TileLayer) tileLayers += 1;
    });
    expect(tileLayers).toBe(0);
    // And no third-party tile host anywhere in the DOM it produced.
    expect(el.innerHTML).not.toMatch(/cartocdn|<img/);
    map.remove();
    el.remove();
  });

  it("adds the static OpenStreetMap attribution, with or without a default control", () => {
    for (const attributionControl of [true, false]) {
      const el = document.createElement("div");
      document.body.appendChild(el);
      const map = L.map(el, { attributionControl }).setView([40, -100], 5);
      addTilesUnavailableNotice(map);
      expect(el.textContent).toContain(OSM_ATTRIBUTION);
      map.remove();
      el.remove();
    }
  });
});
