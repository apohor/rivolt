// DrivesOverviewMap draws every drive's route on one MapLibre map. A drive with a stored polyline (the live recorder) shows its real GPS trace;
// a legacy one (ElectraFi import) starts as a straight start→end line and is replaced by a road-snapped route from Valhalla, a few at a time.
// Routes are one GeoJSON line layer (with a wide invisible one to hover and click), the start (green) and end (rose) dots a circle layer.
import { useEffect, useMemo, useRef } from "react";
import type { GeoJSONSource } from "maplibre-gl";
import { ensureConfig, valhallaBase } from "../lib/config";
import { displayTimeZone } from "../lib/format";
import { decodePolyline } from "../lib/polyline";
import { loadMapLibre } from "./loader";
import { mountBaseMap } from "./mount";
import { valhallaRoutePair } from "./snap";
import { MapUnavailable } from "./Unavailable";
import { webglAvailable } from "./webgl";

const ROUTE_CONCURRENCY = 20;
// A drive line: wide enough to read on a real basemap, with round caps; on the light map a thin casing in the page colour keeps overlapping
// drives apart.
const ROUTE_WIDTH = 2.5;
const ROUTE_OPACITY = 0.85;
const LIGHT_CASING = "#F6F7FA";
const LINES = "rivolt-drives";
const ENDS = "rivolt-drive-ends";

// routeOverview fetches a road-snapped start→end geometry for a drive without a stored polyline: two coordinates, no GPS trace. Null when
// Valhalla is not wired or the call fails, so the caller keeps the straight line.
async function routeOverview(sLat: number, sLon: number, eLat: number, eLon: number, signal: AbortSignal): Promise<[number, number][] | null> {
  await ensureConfig();
  if (signal.aborted) return null;
  if (valhallaBase() === "") return null;
  return valhallaRoutePair([sLat, sLon], [eLat, eLon], signal);
}

export function DrivesOverviewMap({
  drives,
  onSelect,
  height = 360,
}: {
  drives: {
    ID: string;
    StartLat: number;
    StartLon: number;
    EndLat: number;
    EndLon: number;
    StartedAt: string;
    DistanceMi: number;
    RoutePolyline?: string;
  }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const sig = drives.map((d) => d.ID).join("|");

  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    const valid = drives.filter(
      (d) =>
        Number.isFinite(d.StartLat) && Number.isFinite(d.StartLon) && Number.isFinite(d.EndLat) && Number.isFinite(d.EndLon) &&
        (d.StartLat !== 0 || d.StartLon !== 0) && (d.EndLat !== 0 || d.EndLon !== 0),
    );
    if (valid.length === 0) return;

    let alive = true;
    const ac = new AbortController();
    const cleanups: (() => void)[] = [() => { alive = false; ac.abort(); }];
    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;
      const m = mountBaseMap(ml, el, { center: [valid[0].StartLon, valid[0].StartLat], zoom: 4 });
      const { map } = m;
      cleanups.push(() => m.dispose());

      type Line = { type: "Feature"; geometry: { type: "LineString"; coordinates: [number, number][] }; properties: { id: string } };
      const lines: Line[] = [];
      const ends: GeoJSON.Feature[] = [];
      const tips = new Map<string, string>();
      const needsRoute: { d: (typeof valid)[0]; line: Line }[] = [];
      const all: [number, number][] = [];
      for (const d of valid) {
        const start: [number, number] = [d.StartLat, d.StartLon];
        const end: [number, number] = [d.EndLat, d.EndLon];
        const trace = decodePolyline(d.RoutePolyline);
        const path: [number, number][] = trace.length >= 2 ? trace : [start, end];
        for (const p of path) all.push(p);
        const when = new Date(d.StartedAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() });
        tips.set(d.ID, `<div style="font:11px/1.3 ui-sans-serif,system-ui"><div style="color:#34d399;font-weight:600">${d.DistanceMi.toFixed(1)} mi</div><div style="color:#a3a3a3">${when}</div></div>`);
        const line: Line = { type: "Feature", geometry: { type: "LineString", coordinates: path.map(([la, lo]) => [lo, la] as [number, number]) }, properties: { id: d.ID } };
        lines.push(line);
        ends.push(
          { type: "Feature", geometry: { type: "Point", coordinates: [d.StartLon, d.StartLat] }, properties: { id: d.ID, color: "#10b981" } },
          { type: "Feature", geometry: { type: "Point", coordinates: [d.EndLon, d.EndLat] }, properties: { id: d.ID, color: "#f43f5e" } },
        );
        if (trace.length < 2) needsRoute.push({ d, line });
      }
      const lineData = () => ({ type: "FeatureCollection" as const, features: lines });
      const endData = { type: "FeatureCollection" as const, features: ends };

      m.onStyle((flavor) => {
        if (!map.getSource(LINES)) map.addSource(LINES, { type: "geojson", data: lineData() as never });
        else (map.getSource(LINES) as GeoJSONSource).setData(lineData() as never);
        if (!map.getSource(ENDS)) map.addSource(ENDS, { type: "geojson", data: endData as never });
        if (!map.getLayer("rivolt-drives-hit")) {
          const round = { "line-cap": "round", "line-join": "round" } as const;
          map.addLayer({ id: "rivolt-drives-hit", type: "line", source: LINES, paint: { "line-width": 12, "line-opacity": 0 } });
          map.addLayer({ id: "rivolt-drives-casing", type: "line", source: LINES, layout: round, paint: { "line-color": LIGHT_CASING, "line-width": ROUTE_WIDTH + 2, "line-opacity": flavor === "light" ? 0.9 : 0 } });
          map.addLayer({ id: "rivolt-drives-line", type: "line", source: LINES, layout: round, paint: { "line-color": "#34d399", "line-width": ROUTE_WIDTH, "line-opacity": flavor === "light" ? 1 : ROUTE_OPACITY } });
          map.addLayer({ id: "rivolt-drives-ends", type: "circle", source: ENDS, paint: { "circle-radius": 5, "circle-color": ["get", "color"], "circle-stroke-color": "#0a0a0a", "circle-stroke-width": 1.5 } });
        }
      });

      const tip = new ml.Popup({ closeButton: false, closeOnClick: false, offset: 12, className: "rivolt-tip", maxWidth: "260px" });
      for (const layer of ["rivolt-drives-hit", "rivolt-drives-ends"]) {
        map.on("mousemove", layer, (e) => {
          const id = e.features?.[0]?.properties?.id;
          if (id == null) return;
          map.getCanvas().style.cursor = "pointer";
          const html = tips.get(String(id));
          if (html) tip.setLngLat(e.lngLat).setHTML(html).addTo(map);
        });
        map.on("mouseleave", layer, () => { map.getCanvas().style.cursor = ""; tip.remove(); });
        map.on("click", layer, (e) => {
          const id = e.features?.[0]?.properties?.id;
          if (id != null) onSelectRef.current?.(String(id));
        });
      }
      cleanups.push(() => tip.remove());

      const lons = all.map((p) => p[1]), lats = all.map((p) => p[0]);
      map.fitBounds([[Math.min(...lons), Math.min(...lats)], [Math.max(...lons), Math.max(...lats)]], { padding: 24, maxZoom: 14, animate: false });

      // Replace the straight placeholders with road-snapped routes, ROUTE_CONCURRENCY at a time; one redraw per frame however many arrive.
      if (needsRoute.length > 0) {
        let raf = 0;
        const redraw = () => {
          raf = 0;
          const src = map.getSource(LINES) as GeoJSONSource | undefined;
          if (alive && src) src.setData(lineData() as never);
        };
        cleanups.push(() => { if (raf) cancelAnimationFrame(raf); });
        const queue = [...needsRoute];
        const worker = async () => {
          while (queue.length > 0 && !ac.signal.aborted) {
            const item = queue.shift()!;
            const routed = await routeOverview(item.d.StartLat, item.d.StartLon, item.d.EndLat, item.d.EndLon, ac.signal);
            if (routed && routed.length >= 2 && !ac.signal.aborted) {
              item.line.geometry.coordinates = routed.map(([la, lo]) => [lo, la] as [number, number]);
              if (!raf) raf = requestAnimationFrame(redraw);
            }
          }
        };
        void Promise.all(Array.from({ length: Math.min(ROUTE_CONCURRENCY, needsRoute.length) }, worker));
      }
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));
    return () => { for (const c of cleanups.reverse()) c(); };
    // The map is rebuilt only when the visible set changes (sig), not on every parent render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [supported, sig]);

  if (!supported) return <MapUnavailable height={height} note="The list beside it is unaffected." />;
  return <div ref={ref} className="rounded-lg overflow-hidden border border-neutral-800" style={{ height }} />;
}
