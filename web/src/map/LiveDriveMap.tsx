// LiveDriveMap: the drive in progress on the self-hosted basemap (design_preview "live_v2"): the trail since the drive's start and the truck's
// dot at its latest fix. The map is made once; each new batch of points replaces the trail in place and eases the camera after the truck when it
// nears the edge. Once the person pans or zooms, the camera stays where they put it until "Follow the truck" is pressed. MapLibre loads as its
// own chunk (loader.ts); without WebGL it says so (the figures beside it are unaffected).
import { useEffect, useRef, useState } from "react";
import type { Map as MLMap, Marker } from "maplibre-gl";
import { ensureConfig, tilesPMTilesURL } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { loadMapLibre } from "./loader";
import { webglAvailable } from "./webgl";
import { styleFor } from "./base";
import { MapUnavailable, TILES_UNAVAILABLE_TEXT } from "./Unavailable";
import { BASEMAP_ATTRIBUTION } from "./style";
import { boundsOf, firstLabelLayer, setDriveRoute, ROUTE_UNIFORM, type LatLng } from "./route";
import { pinElement, foldCredits, tilesNotice } from "./controls";
import { watchDrawn } from "./drawn";

export type LivePoint = { lat: number; lon: number };

export function LiveDriveMap({ points, height = 280 }: { points: LivePoint[]; height?: number }) {
  const supported = useRef(webglAvailable()).current;
  const el = useRef<HTMLDivElement | null>(null);
  const api = useRef<{ update: (p: LivePoint[]) => void; follow: () => void } | null>(null);
  const latest = useRef(points);
  latest.current = points;
  const [following, setFollowing] = useState(true);

  useEffect(() => {
    if (!supported || !el.current) return;
    const box = el.current;
    let alive = true;
    const cleanups: (() => void)[] = [];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !box.isConnected) return;
      let flavor = appMapFlavor();
      const first = latest.current[latest.current.length - 1] ?? { lat: 0, lon: 0 };
      const map: MLMap = new ml.Map({ container: box, style: styleFor(flavor), center: [first.lon, first.lat], zoom: 14, attributionControl: false, dragRotate: false, pitchWithRotate: false, touchPitch: false, fadeDuration: 150 });
      box.setAttribute("data-flavor", flavor);
      map.touchZoomRotate.disableRotation();
      map.scrollZoom.disable();
      map.on("click", () => map.scrollZoom.enable());
      map.on("mouseout", () => map.scrollZoom.disable());
      map.on("error", (e) => {
        const err = (e as unknown as { error?: { status?: number; message?: string } }).error;
        if (err?.status !== 404) console.warn("map:", err?.message ?? "error");
      });
      map.addControl(new ml.NavigationControl({ showCompass: false }), "top-left");
      map.addControl(new ml.AttributionControl({ compact: true, customAttribution: BASEMAP_ATTRIBUTION }), "bottom-right");
      foldCredits(box);
      if (!tilesPMTilesURL().startsWith("/")) map.addControl(tilesNotice(TILES_UNAVAILABLE_TEXT), "bottom-left");

      let styleReady = false;
      let followTruck = true;
      let framed = false;
      let truck: Marker | null = null;
      const drawn = watchDrawn(map, box, "rivolt-track-main");
      cleanups.push(drawn.stop);

      const draw = () => {
        const pts = latest.current.filter((p) => Number.isFinite(p.lat) && Number.isFinite(p.lon) && (p.lat !== 0 || p.lon !== 0));
        const track: LatLng[] = pts.map((p) => [p.lat, p.lon]);
        const here = pts[pts.length - 1];
        if (styleReady) {
          setDriveRoute(map, { track, stops: [0, ROUTE_UNIFORM, 1, ROUTE_UNIFORM] }, flavor, firstLabelLayer(map));
          drawn.markDrawn();
        }
        if (!here) return;
        if (!truck) truck = new ml.Marker({ element: pinElement("#38bdf8", "Truck") }).setLngLat([here.lon, here.lat]).addTo(map);
        else truck.setLngLat([here.lon, here.lat]);
        if (!followTruck) return;
        if (!framed) {
          const b = boundsOf(track);
          if (b && box.clientWidth > 0) map.fitBounds(b, { padding: 48, maxZoom: 16, animate: false });
          else map.jumpTo({ center: [here.lon, here.lat], zoom: 15 });
          framed = true;
          return;
        }
        // Ease after the truck once it leaves the middle of the map.
        const p = map.project([here.lon, here.lat]);
        const w = box.clientWidth, h = box.clientHeight;
        if (p.x < w * 0.2 || p.x > w * 0.8 || p.y < h * 0.2 || p.y > h * 0.8) map.easeTo({ center: [here.lon, here.lat], duration: 600 });
      };

      map.on("style.load", () => { styleReady = true; draw(); });
      cleanups.push(onAppMapFlavor((f) => { flavor = f; box.setAttribute("data-flavor", f); styleReady = false; map.setStyle(styleFor(f)); }));
      // A gesture by the person (not the app's own ease) ends the following.
      const stop = (e: { originalEvent?: unknown }) => { if (e.originalEvent) { followTruck = false; setFollowing(false); } };
      map.on("dragstart", stop);
      map.on("zoomstart", stop);
      api.current = {
        update: draw,
        follow: () => {
          followTruck = true;
          framed = false;
          setFollowing(true);
          draw();
        },
      };
      cleanups.push(() => { api.current = null; truck?.remove(); map.remove(); });
    })();
    return () => { alive = false; cleanups.forEach((c) => c()); };
  }, [supported]);

  useEffect(() => { api.current?.update(points); }, [points]);

  if (!supported) return <MapUnavailable height={height} note="The drive's figures beside it are unaffected." />;
  return (
    <div className="relative" data-testid="live-drive-map">
      <div ref={el} className="overflow-hidden rounded-lg border border-neutral-800" style={{ height }} />
      {!following ? (
        <button
          type="button"
          onClick={() => api.current?.follow()}
          className="absolute bottom-8 left-2 min-h-11 rounded-full border border-neutral-700 bg-neutral-900/90 px-3 text-xs font-medium text-neutral-100 hover:border-emerald-600 pointer-fine:min-h-8"
        >
          Follow the truck
        </button>
      ) : null}
    </div>
  );
}
