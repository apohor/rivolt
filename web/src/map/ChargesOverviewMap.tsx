// ChargesOverviewMap draws one dot per charge on the self-hosted MapLibre basemap, coloured by power class, with a hover tooltip summarising
// the session; a click calls onSelect (the detail page). Pins at one spot (home charging) stack; they are not clustered. The dots are one
// GeoJSON circle layer, so hundreds of charges cost no DOM nodes. Without WebGL it says the map is unavailable.
import { useEffect, useMemo, useRef } from "react";
import type { GeoJSONSource } from "maplibre-gl";
import { displayTimeZone } from "../lib/format";
import { ensureConfig } from "../lib/config";
import { chipLegend } from "./controls";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { MapUnavailable } from "./Unavailable";
import { webglAvailable } from "./webgl";

// ChargeBucket categorizes a session by power.
//   - "fast": kind dcfc (else ≥ 50 kW peak)
//   - "l2":   3 kW peak or more, AC (240 V, public or home)
//   - "l1":   < 3 kW peak (110 V trickle / unknown)
// "fast" is the server's DC fast charge (kind, the ledger's rule, as on Money and every other page); only without a classification does it fall
// back to peak power.
export function chargeBucket(c: { MaxPowerKW: number; kind?: "ac" | "dcfc" }): "fast" | "l2" | "l1" {
  if (c.kind === "dcfc") return "fast";
  const maxKW = c.MaxPowerKW;
  if (c.kind === undefined && Number.isFinite(maxKW) && maxKW >= 50) return "fast";
  if (!Number.isFinite(maxKW) || maxKW < 3) return "l1";
  return "l2";
}

export const CHARGE_BUCKET_COLOR: Record<"fast" | "l2" | "l1", string> = {
  fast: "#f43f5e",
  l2: "#06b6d4",
  l1: "#a3a3a3",
};

const BUCKET_LABEL = { fast: "Fast charging", l2: "Level 2", l1: "Household outlet (Level 1)" } as const;
const SOURCE = "rivolt-charges";
const LAYER = "rivolt-charges-dots";

export function ChargesOverviewMap({
  charges,
  onSelect,
  height = 360,
}: {
  charges: { ID: string; Lat: number; Lon: number; StartedAt: string; EnergyAddedKWh: number; MaxPowerKW: number }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  // The latest onSelect, so the click handler never goes stale (the map mounts once per visible-charge set).
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  // Rebuild only when the visible set changes, not on every parent render.
  const sig = charges.map((c) => `${c.ID}:${c.Lat.toFixed(5)},${c.Lon.toFixed(5)}`).join("|");

  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    const valid = charges.filter((c) => Number.isFinite(c.Lat) && Number.isFinite(c.Lon) && (c.Lat !== 0 || c.Lon !== 0));
    if (valid.length === 0) return;

    let alive = true;
    const cleanups: (() => void)[] = [() => { alive = false; }];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const m = mountBaseMap(ml, el, { center: [valid[0].Lon, valid[0].Lat], zoom: 4 });
      const { map } = m;
      cleanups.push(() => m.dispose());

      const tips = new Map<string, string>();
      const data = {
        type: "FeatureCollection" as const,
        features: valid.map((c) => {
          const bucket = chargeBucket(c);
          const when = new Date(c.StartedAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() });
          tips.set(
            c.ID,
            `<div style="font:11px/1.3 ui-sans-serif,system-ui">` +
              `<div style="color:${CHARGE_BUCKET_COLOR[bucket]};font-weight:600">${BUCKET_LABEL[bucket]} · ${c.MaxPowerKW.toFixed(1)} kW</div>` +
              `<div style="color:#a3a3a3">${when}</div>` +
              `<div style="color:#d4d4d4">${c.EnergyAddedKWh.toFixed(1)} kWh added</div></div>`,
          );
          return { type: "Feature" as const, geometry: { type: "Point" as const, coordinates: [c.Lon, c.Lat] }, properties: { id: c.ID, color: CHARGE_BUCKET_COLOR[bucket] } };
        }),
      };

      const legend = chipLegend((["fast", "l2", "l1"] as const).map((k) => ({ color: CHARGE_BUCKET_COLOR[k], label: BUCKET_LABEL[k] })));
      map.addControl(legend, "bottom-left");
      m.onStyle((flavor) => {
        legend.refresh(flavor);
        if (!map.getSource(SOURCE)) map.addSource(SOURCE, { type: "geojson", data: data as never });
        else (map.getSource(SOURCE) as GeoJSONSource).setData(data as never);
        if (!map.getLayer(LAYER)) {
          map.addLayer({
            id: LAYER,
            type: "circle",
            source: SOURCE,
            paint: { "circle-radius": 5, "circle-color": ["get", "color"], "circle-stroke-color": "#0a0a0a", "circle-stroke-width": 1.5 },
          });
        }
      });

      const tip = new ml.Popup({ closeButton: false, closeOnClick: false, offset: 10, className: "rivolt-tip", maxWidth: "260px" });
      map.on("mousemove", LAYER, (e) => {
        const f = e.features?.[0];
        if (!f) return;
        map.getCanvas().style.cursor = "pointer";
        const html = tips.get(String(f.properties?.id));
        if (html) tip.setLngLat((f.geometry as GeoJSON.Point).coordinates as [number, number]).setHTML(html).addTo(map);
      });
      map.on("mouseleave", LAYER, () => { map.getCanvas().style.cursor = ""; tip.remove(); });
      map.on("click", LAYER, (e) => {
        const id = e.features?.[0]?.properties?.id;
        if (id != null) onSelectRef.current?.(String(id));
      });
      cleanups.push(() => tip.remove());

      const lons = valid.map((c) => c.Lon), lats = valid.map((c) => c.Lat);
      map.fitBounds([[Math.min(...lons), Math.min(...lats)], [Math.max(...lons), Math.max(...lats)]], { padding: 24, maxZoom: 14, animate: false });
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));
    return () => { for (const c of cleanups.reverse()) c(); };
    // The map is rebuilt only when the visible set changes (sig), not on every parent render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [supported, sig]);

  if (!supported) return <MapUnavailable height={height} note="The list beside it is unaffected." />;
  return <div ref={ref} className="rounded-lg overflow-hidden border border-neutral-800" style={{ height }} />;
}
