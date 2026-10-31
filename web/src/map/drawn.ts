// data-map-drawn: how many features of `layerId` the map is rendering in view, written on the container once the line is there (0 while it is
// not). A spec waits for a non-zero value before it takes a screenshot, so a shot is of a drawn route, not of markers alone. Not the library's
// "idle", which waits for every tile and never comes while a source keeps retrying a tile the server lacks. markDrawn() restarts the watch
// (call it whenever the route is replaced); the returned stop removes the listener and the attribute.
import type { Map as MLMap } from "maplibre-gl";

export function watchDrawn(map: MLMap, el: HTMLElement, layerId: string): { markDrawn: () => void; stop: () => void } {
  let watching: (() => void) | undefined;
  const count = () => {
    try {
      return map.getLayer(layerId) ? map.queryRenderedFeatures({ layers: [layerId] }).length : 0;
    } catch {
      return 0;
    }
  };
  const markDrawn = () => {
    if (watching) map.off("render", watching);
    el.setAttribute("data-map-drawn", "0");
    const h = () => {
      const n = count();
      if (n > 0) {
        el.setAttribute("data-map-drawn", String(n));
        map.off("render", h);
        if (watching === h) watching = undefined;
      }
    };
    watching = h;
    map.on("render", h);
    map.triggerRepaint();
  };
  const stop = () => {
    if (watching) map.off("render", watching);
    watching = undefined;
    el.removeAttribute("data-map-drawn");
  };
  return { markDrawn, stop };
}
