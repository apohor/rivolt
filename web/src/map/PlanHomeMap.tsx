// PlanHomeMap: the map the planner opens on (design_preview "plan_where_to"): the truck's position on the self-hosted basemap, with the live radar
// when the page asks for it. It fills its parent, which lays the search and the trips over it. Without a position it shows the continental US.
// Without WebGL it says so in the parent's size.
import { useEffect, useMemo, useRef } from "react";
import type { Map as MLMap, Marker } from "maplibre-gl";
import { ensureConfig } from "../lib/config";
import { pinElement } from "./controls";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { fetchRadarFrame, hideRadar, RADAR_DENIED, RADAR_REFRESH_MS, radarAsOf, showRadar } from "./radar";
import { webglAvailable } from "./webgl";

const US_CENTER: [number, number] = [-98, 39];

export function PlanHomeMap({
  truck,
  radar,
  onRadar,
}: {
  truck: { lat: number; lon: number } | null;
  radar: boolean;
  // The radar strip's text ("NOAA/NWS · 6:42 PM"), "" while it loads or when it is off, "denied" when this visitor is not served radar.
  onRadar: (label: string) => void;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const live = useRef<{ map: MLMap; marker: Marker | null; ready: () => boolean } | null>(null);
  const start = useRef(truck);
  const onRadarRef = useRef(onRadar);
  onRadarRef.current = onRadar;
  // The map exists once: a state flips when it is up so the radar effect can run.
  const upRef = useRef<(() => void) | null>(null);
  const radarWanted = useRef(radar);
  radarWanted.current = radar;

  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    let alive = true;
    const cleanups: (() => void)[] = [() => { alive = false; live.current = null; }];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const at = start.current ? ([start.current.lon, start.current.lat] as [number, number]) : null;
      const m = mountBaseMap(ml, el, { center: at ?? US_CENTER, zoom: at ? 9 : 3.4 });
      cleanups.push(() => m.dispose());
      const marker = at ? new ml.Marker({ element: pinElement("#10b981", "Your truck") }).setLngLat(at).addTo(m.map) : null;
      live.current = { map: m.map, marker, ready: m.styleReady };
      upRef.current?.();
      upRef.current = null;
    })();
    return () => cleanups.forEach((c) => c());
  }, [supported]);

  // A new fix moves the pin (and the map, until the person has moved it themselves: the first fix after a cold start is the one that matters).
  useEffect(() => {
    const m = live.current;
    if (!m || !truck) return;
    const at: [number, number] = [truck.lon, truck.lat];
    if (m.marker) m.marker.setLngLat(at);
    else {
      void loadMapLibre().then((ml) => {
        if (live.current === m && !m.marker) {
          m.marker = new ml.Marker({ element: pinElement("#10b981", "Your truck") }).setLngLat(at).addTo(m.map);
          m.map.jumpTo({ center: at, zoom: 9 });
        }
      });
    }
    // Keyed on the coordinates, not the object, so a refetch with the same fix does not move the map.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [truck?.lat, truck?.lon]);

  useEffect(() => {
    if (!supported) return;
    let alive = true;
    const ac = new AbortController();
    let timer = 0;
    let cleanup: (() => void) | null = null;
    const run = () => {
      const m = live.current;
      if (!m) return false;
      const draw = (t: number) => {
        if (m.ready()) showRadar(m.map, t);
        else m.map.once("style.load", () => alive && showRadar(m.map, t));
      };
      const tick = async () => {
        const f = await fetchRadarFrame(ac.signal);
        if (!alive) return;
        if (f === RADAR_DENIED) { hideRadar(m.map); onRadarRef.current("denied"); return; }
        if (!f) { hideRadar(m.map); onRadarRef.current("Radar unavailable"); return; }
        draw(f.frameUnix);
        onRadarRef.current(radarAsOf(f.frameUnix));
      };
      void tick();
      timer = window.setInterval(() => void tick(), RADAR_REFRESH_MS);
      cleanup = () => { window.clearInterval(timer); hideRadar(m.map); onRadarRef.current(""); };
      return true;
    };
    if (radar) {
      if (!run()) upRef.current = () => { if (alive && radarWanted.current) run(); };
    }
    return () => { alive = false; ac.abort(); cleanup?.(); };
  }, [supported, radar]);

  if (!supported)
    return (
      <div role="status" data-map-unavailable className="absolute inset-0 flex items-start justify-center bg-neutral-900/50 px-4 pt-40 text-center text-sm text-neutral-400">
        The map can&apos;t be drawn in this browser (WebGL is off or unsupported).
      </div>
    );
  // "!absolute": MapLibre's stylesheet gives .maplibregl-map position: relative, which would otherwise win and collapse this fill to no height.
  return <div ref={ref} data-testid="plan-home-map" className="!absolute inset-0 [&_.maplibregl-ctrl-top-left]:hidden" />;
}
