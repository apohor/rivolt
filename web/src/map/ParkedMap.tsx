// ParkedMap: the truck's parked position on the self-hosted basemap, for the Now hero's parked slot (design_preview "now_slot"). One pin, no
// panning: the card around it is a link to Full telemetry. Without WebGL it says so in the box's own size.
import { useEffect, useMemo, useRef } from "react";
import { ensureConfig } from "../lib/config";
import { pinElement } from "./controls";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { MapUnavailable } from "./Unavailable";
import { webglAvailable } from "./webgl";

export function ParkedMap({ lat, lon, height = 150 }: { lat: number; lon: number; height?: number }) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    let alive = true;
    const cleanups: (() => void)[] = [() => { alive = false; }];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const m = mountBaseMap(ml, el, { center: [lon, lat], zoom: 14 });
      cleanups.push(() => m.dispose());
      new ml.Marker({ element: pinElement("#10b981", "Parked here") }).setLngLat([lon, lat]).addTo(m.map);
    })();
    return () => cleanups.forEach((c) => c());
  }, [supported, lat, lon]);
  if (!supported) return <MapUnavailable height={height} note="" />;
  return <div ref={ref} data-testid="now-slot-map" className="w-full overflow-hidden rounded-xl border border-neutral-800 [&_.maplibregl-ctrl-top-left]:hidden" style={{ height }} />;
}
