// DriveMap draws one drive's route on the self-hosted MapLibre basemap: the recorded GPS trace first, replaced by the road-snapped route when
// the Valhalla proxy returns it; speed-band colouring, start and end pins, the chart-scrub dot, hover-to-time, the speed legend and the GPS
// staleness badge. MapLibre, its worker and the PMTiles reader load as their own chunk when this mounts (loader.ts). Without WebGL it says
// the map is unavailable and leaves the rest of the page standing.
import { useEffect, useMemo, useRef } from "react";
import type { Map as MLMap, Marker } from "maplibre-gl";
import { ensureConfig, tilesDEM, tilesGlyphsURL, tilesPMTilesURL, valhallaBase } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { loadMapLibre } from "./loader";
import { webglAvailable } from "./webgl";
import { buildStyle, plainStyle, type MapFlavor } from "./style";
import { boundsOf, routeData, setRoute, speedRuns, type LatLng } from "./route";
import { snapToRoads, type SnapPoint } from "./snap";
import { cursorElement, flavorControl, pinElement, speedLegend, stalenessBadge, tilesNotice } from "./controls";

type Point = { lat: number; lon: number; t?: number; s?: number };

export const TILES_UNAVAILABLE_TEXT = "Map tiles unavailable on this server";
const OSM_ATTRIBUTION = "© OpenStreetMap contributors";

// MapUnavailable is what the card shows when this browser cannot draw WebGL: plain words, the box kept so the layout does not jump.
export function MapUnavailable({ height }: { height: number }) {
  return (
    <div
      role="status"
      data-map-unavailable
      className="flex items-center justify-center rounded-lg border border-neutral-800 bg-neutral-900/50 px-4 text-center text-sm text-neutral-400"
      style={{ height }}
    >
      The map can&apos;t be drawn in this browser (WebGL is off or unsupported). The drive&apos;s figures and charts below are unaffected.
    </div>
  );
}

// styleFor is the style for a flavour from what the server advertises: the full basemap when it advertises a tile archive, a plain background
// when it does not.
function styleFor(flavor: MapFlavor) {
  const tiles = tilesPMTilesURL();
  if (!tiles.startsWith("/")) return plainStyle(flavor);
  const dem = tilesDEM();
  return buildStyle({ flavor, origin: location.origin, tilesPath: tiles, glyphsPath: tilesGlyphsURL(), dem: dem.url ? dem : null });
}

export function DriveMap({
  points,
  start,
  end,
  height = 320,
  cursorTime,
  onCursorChange,
  fixAgeSeconds,
}: {
  points: Point[];
  start?: Point;
  end?: Point;
  height?: number;
  // Controlled cursor in unix seconds: an accent dot sits on the sample nearest this time (the chart hover).
  cursorTime?: number | null;
  // Fires with the time of the sample nearest the pointer, when the pointer is within 28 px of the route, and null on leaving.
  onCursorChange?: (t: number | null) => void;
  // Worst GNSS fix age across the drive's samples (seconds); at 5 minutes or more a "GPS fix N min stale" badge shows.
  fixAgeSeconds?: number | null;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MLMap | null>(null);
  const timedRef = useRef<Required<Point>[]>([]);
  const onCursorChangeRef = useRef(onCursorChange);
  onCursorChangeRef.current = onCursorChange;
  const cursorRef = useRef<Marker | null>(null);

  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    const valid = points.filter((p) => Number.isFinite(p.lat) && Number.isFinite(p.lon) && (p.lat !== 0 || p.lon !== 0));
    // The most recent drive often has no end telemetry yet, so the caller's start/end fallback can be NaN: validate before it reaches the map.
    const finite = (p?: Point): Point | undefined => (p && Number.isFinite(p.lat) && Number.isFinite(p.lon) ? p : undefined);
    const startPt = finite(start);
    const endPt = finite(end);
    const fallback: Point | undefined = startPt ?? endPt ?? valid[0];
    if (valid.length === 0 && !fallback) return;

    const lineStart: Point | undefined = startPt ?? valid[0];
    const lineEnd: Point | undefined = endPt ?? valid[valid.length - 1];
    // A drive that starts and ends in the same spot (within ~50 m) shows one pin.
    const sameSpot = !!lineStart && !!lineEnd && Math.abs(lineStart.lat - lineEnd.lat) < 0.0005 && Math.abs(lineStart.lon - lineEnd.lon) < 0.0005;

    // The trace, extended out to the parked start/end when we have them, so the route connects to its pins.
    const latlngs: LatLng[] = [];
    const speeds: (number | undefined)[] = [];
    if (startPt) { latlngs.push([startPt.lat, startPt.lon]); speeds.push(0); }
    for (const p of valid) { latlngs.push([p.lat, p.lon]); speeds.push(p.s); }
    if (endPt && !sameSpot) { latlngs.push([endPt.lat, endPt.lon]); speeds.push(0); }
    // One frozen fix for the whole drive: no route to draw, and fitting zero-extent bounds is meaningless, so centre on the point.
    const SPREAD_EPS = 1e-4; // ~11 m
    const hasSpread = latlngs.some(([la, lo]) => Math.abs(la - latlngs[0][0]) > SPREAD_EPS || Math.abs(lo - latlngs[0][1]) > SPREAD_EPS);

    let alive = true;
    const ac = new AbortController();
    const cleanups: (() => void)[] = [];
    cleanups.push(() => { alive = false; ac.abort(); });

    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;

      let flavor: MapFlavor = appMapFlavor();
      const tiles = tilesPMTilesURL().startsWith("/");
      const centre = fallback ?? valid[0];
      const map = new ml.Map({
        container: el,
        style: styleFor(flavor),
        center: [centre.lon, centre.lat],
        zoom: 13,
        attributionControl: false,
        dragRotate: false,
        pitchWithRotate: false,
        touchPitch: false,
        fadeDuration: 150,
      });
      mapRef.current = map;
      map.touchZoomRotate.disableRotation();
      // Click to enable the wheel; leaving the map disables it again, so the page keeps scrolling over the map.
      map.scrollZoom.disable();
      map.on("click", () => map.scrollZoom.enable());
      map.on("mouseout", () => map.scrollZoom.disable());
      // A terrain or label tile the server does not hold is a 404: the map draws without it, quietly.
      map.on("error", (e) => {
        const err = (e as unknown as { error?: { status?: number; message?: string } }).error;
        if (err?.status !== 404) console.warn("map:", err?.message ?? "error");
      });

      map.addControl(new ml.NavigationControl({ showCompass: false }), "top-left");
      map.addControl(new ml.AttributionControl({ compact: true, customAttribution: tiles ? undefined : OSM_ATTRIBUTION }), "bottom-right");
      // The credits (terrain's is long) open by default; on a phone they would fill the map's corner over the route, so start them folded.
      if (el.clientWidth < 640) {
        const attrib = el.querySelector(".maplibregl-ctrl-attrib");
        attrib?.classList.remove("maplibregl-compact-show");
        attrib?.removeAttribute("open");
      }
      if (!tiles) map.addControl(tilesNotice(TILES_UNAVAILABLE_TEXT), "bottom-left");

      // The D/L picker: a temporary override of the basemap that the app theme resets.
      const setFlavor = (f: MapFlavor) => {
        if (f === flavor) return;
        flavor = f;
        map.setStyle(styleFor(f));
      };
      const picker = flavorControl(flavor, (f) => { setFlavor(f); picker.refresh(f); });
      map.addControl(picker, "top-right");
      cleanups.push(onAppMapFlavor((f) => { setFlavor(f); picker.refresh(f); }));

      const hasSpeed = speeds.some((s) => s != null && Number.isFinite(s));
      if (hasSpeed) map.addControl(speedLegend(speeds), "bottom-left");
      if (typeof fixAgeSeconds === "number" && Number.isFinite(fixAgeSeconds) && fixAgeSeconds >= 5 * 60) map.addControl(stalenessBadge(fixAgeSeconds), "top-right");

      // What to keep framed: the raw trace first, the snapped route once it arrives; every resize refits to it.
      let fitBox = hasSpread ? boundsOf(latlngs) : null;
      const fit = () => {
        if (!fitBox || !(el.clientWidth > 0 && el.clientHeight > 0)) return;
        const bottom = el.clientWidth < 520 ? 64 : 44;
        map.fitBounds(fitBox, { padding: { top: 44, left: 20, right: 72, bottom }, animate: false, maxZoom: 17 });
      };

      let data = routeData({ runs: hasSpread ? speedRuns(latlngs, speeds) : [] });
      // The style (re)loads on creation and on every flavour change; the route layers go back on top each time.
      map.on("style.load", () => { setRoute(map, data); });

      if (!hasSpread && latlngs.length > 0) map.jumpTo({ center: [latlngs[0][1], latlngs[0][0]], zoom: 16 });

      const markers: Marker[] = [];
      if (lineStart) markers.push(new ml.Marker({ element: pinElement("#10b981", sameSpot ? "Start / End" : "Start") }).setLngLat([lineStart.lon, lineStart.lat]).addTo(map));
      if (lineEnd && !sameSpot) markers.push(new ml.Marker({ element: pinElement("#f43f5e", "End") }).setLngLat([lineEnd.lon, lineEnd.lat]).addTo(map));
      cleanups.push(() => markers.forEach((m) => m.remove()));

      // Hover to time: the nearest timestamped sample within 28 px of the pointer.
      const timed = valid.filter((p) => Number.isFinite(p.t)) as Required<Point>[];
      timedRef.current = timed;
      if (timed.length > 0) {
        const HOVER_PX = 28;
        map.on("mousemove", (ev) => {
          const cb = onCursorChangeRef.current;
          if (!cb) return;
          let bestI = -1;
          let bestD2 = HOVER_PX * HOVER_PX;
          for (let i = 0; i < timed.length; i++) {
            const q = map.project([timed[i].lon, timed[i].lat]);
            const dx = q.x - ev.point.x;
            const dy = q.y - ev.point.y;
            const d2 = dx * dx + dy * dy;
            if (d2 < bestD2) { bestD2 = d2; bestI = i; }
          }
          if (bestI >= 0) cb(timed[bestI].t);
        });
        map.on("mouseout", () => onCursorChangeRef.current?.(null));
      }

      // Best effort: replace the raw trace with the road-snapped route from Valhalla; keep the raw one on failure.
      const synthT = (t: number | undefined, d: number) => (Number.isFinite(t) ? (t as number) + d : undefined);
      const firstT = valid.find((p) => Number.isFinite(p.t))?.t;
      const lastT = [...valid].reverse().find((p) => Number.isFinite(p.t))?.t;
      const tracePoints: SnapPoint[] = [
        ...(startPt ? [{ ...startPt, t: synthT(firstT, -60) }] : []),
        ...valid,
        ...(endPt && !sameSpot ? [{ ...endPt, t: synthT(lastT, 60) }] : []),
      ];
      if (valhallaBase() !== "" && hasSpread) {
        snapToRoads(tracePoints, ac.signal).then((plan) => {
          if (ac.signal.aborted || mapRef.current !== map || !plan || plan.segments.length === 0) return;
          const runs = [];
          const all: LatLng[] = [];
          for (const { coords: matched } of plan.segments) {
            const matchedSpeeds = matched.map(([lat, lon]) => nearestSpeed(tracePoints, lat, lon));
            runs.push(...speedRuns(matched, matchedSpeeds));
            all.push(...matched);
          }
          for (const r of plan.routedGaps) all.push(...r);
          data = routeData({ runs, routedGaps: plan.routedGaps, gaps: plan.gaps });
          if (map.isStyleLoaded()) setRoute(map, data);
          const b = boundsOf(all);
          if (b) { fitBox = b; fit(); }
        });
      }

      // The container may be sized after mount (a freshly revealed card): resize and refit whenever it changes.
      const ro = new ResizeObserver(() => { if (!alive || mapRef.current !== map) return; map.resize(); fit(); });
      ro.observe(el);
      cleanups.push(() => ro.disconnect());
      map.once("load", fit);
      cleanups.push(() => {
        cursorRef.current?.remove();
        cursorRef.current = null;
        map.remove();
        mapRef.current = null;
      });
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));

    return () => { for (const c of cleanups.reverse()) c(); };
    // points is an array derived upstream; re-run only when its identity or the endpoints change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [supported, points, start?.lat, start?.lon, end?.lat, end?.lon, fixAgeSeconds]);

  // The scrub dot follows cursorTime without rebuilding the map.
  useEffect(() => {
    const map = mapRef.current;
    const timed = timedRef.current;
    if (!map) return;
    if (cursorTime == null || !Number.isFinite(cursorTime) || timed.length === 0) {
      cursorRef.current?.remove();
      cursorRef.current = null;
      return;
    }
    let best = timed[0];
    let bestD = Math.abs(timed[0].t - cursorTime);
    for (let i = 1; i < timed.length; i++) {
      const d = Math.abs(timed[i].t - cursorTime);
      if (d < bestD) { bestD = d; best = timed[i]; }
    }
    if (cursorRef.current) {
      cursorRef.current.setLngLat([best.lon, best.lat]);
    } else {
      void loadMapLibre().then((ml) => {
        if (mapRef.current !== map) return;
        cursorRef.current = new ml.Marker({ element: cursorElement() }).setLngLat([best.lon, best.lat]).addTo(map);
      });
    }
  }, [cursorTime, points]);

  if (!supported) return <MapUnavailable height={height} />;
  return <div ref={ref} className="rounded-lg overflow-hidden border border-neutral-800" style={{ height }} />;
}

// nearestSpeed is the speed of the recorded sample nearest a snapped coordinate (flat-earth distance), so the snapped line keeps its colours.
function nearestSpeed(pts: SnapPoint[], lat: number, lon: number): number | undefined {
  let bestI = 0;
  let bestD = Infinity;
  const cos = Math.cos((lat * Math.PI) / 180);
  for (let i = 0; i < pts.length; i++) {
    const dy = (pts[i].lat - lat) * 111111;
    const dx = (pts[i].lon - lon) * 111111 * cos;
    const d = dx * dx + dy * dy;
    if (d < bestD) { bestD = d; bestI = i; }
  }
  return pts[bestI].s;
}
