// The MapLibre loader. maplibre-gl, its stylesheet, its worker and the PMTiles protocol load as their own chunk, only when a map mounts: the
// main bundle and the pages without a map do not carry it. The worker is the same-origin file Vite emits (setWorkerUrl with the ?url asset),
// so the page's CSP needs no blob: worker and no 'unsafe-eval'.
import type * as MapLibre from "maplibre-gl";

export type MapLibreModule = typeof MapLibre;

let loaded: Promise<MapLibreModule> | undefined;

export function loadMapLibre(): Promise<MapLibreModule> {
  loaded ??= (async () => {
    const [ml, pm, , , worker] = await Promise.all([
      import("maplibre-gl"),
      import("pmtiles"),
      import("maplibre-gl/dist/maplibre-gl.css"),
      import("./map.css"),
      import("maplibre-gl/dist/maplibre-gl-worker.mjs?url"),
    ]);
    const maplibre = ml as MapLibreModule;
    maplibre.setWorkerUrl((worker as { default: string }).default);
    // pmtiles:// URLs read the archive by byte range through the same-origin tiles route.
    const protocol = new pm.Protocol();
    maplibre.addProtocol("pmtiles", protocol.tile);
    return maplibre;
  })();
  return loaded;
}
