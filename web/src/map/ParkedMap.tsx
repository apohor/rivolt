// ParkedMap: the truck's parked position on the self-hosted basemap, for the Now hero's parked slot (design_preview "now_slot"). One pin, no
// panning: the card around it is a link to Full telemetry, so the zoom buttons are hidden. A new fix moves the pin; the map stays.
// Without WebGL it says so in the box's own size. The label (a saved place such as "Home") and the caption sit on the map's corners.
import { useEffect, useMemo, useRef } from "react";
import type { Map as MLMap, Marker } from "maplibre-gl";
import { ensureConfig } from "../lib/config";
import { pinElement } from "./controls";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { MapUnavailable } from "./Unavailable";
import { webglAvailable } from "./webgl";

export function ParkedMap({ lat, lon, label, caption, height = 150 }: { lat: number; lon: number; label?: string | null; caption?: string | null; height?: number }) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const pos = useRef({ lat, lon });
  const live = useRef<{ map: MLMap; marker: Marker } | null>(null);
  pos.current = { lat, lon };
  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    let alive = true;
    const cleanups: (() => void)[] = [() => { alive = false; live.current = null; }];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const at: [number, number] = [pos.current.lon, pos.current.lat];
      const m = mountBaseMap(ml, el, { center: at, zoom: 14 });
      cleanups.push(() => m.dispose());
      const marker = new ml.Marker({ element: pinElement("#10b981", "Parked here") }).setLngLat(at).addTo(m.map);
      live.current = { map: m.map, marker };
    })();
    return () => cleanups.forEach((c) => c());
  }, [supported]);
  useEffect(() => {
    const m = live.current;
    if (!m) return;
    m.marker.setLngLat([lon, lat]);
    m.map.jumpTo({ center: [lon, lat] });
  }, [lat, lon]);
  if (!supported) return <MapUnavailable height={height} note="" />;
  return (
    <div className="relative">
      <div ref={ref} data-testid="now-slot-map" className="w-full overflow-hidden rounded-xl border border-neutral-800 [&_.maplibregl-ctrl-top-left]:hidden" style={{ height }} />
      {label && <span data-testid="now-slot-place" className="pointer-events-none absolute left-2 top-2 rounded-md bg-neutral-900/85 px-1.5 py-0.5 text-xs font-medium text-neutral-100">{label}</span>}
      {caption && <span className="pointer-events-none absolute bottom-1 right-9 text-[11px] text-neutral-600">{caption}</span>}
    </div>
  );
}
