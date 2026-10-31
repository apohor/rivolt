// DriveMap draws one drive's route on the self-hosted MapLibre basemap: the recorded GPS trace first, replaced by the road-snapped route when
// the Valhalla proxy returns it; one smooth gradient coloured by speed, efficiency or elevation (scaled to the drive's own 5th to 95th
// percentile; a mode shows only when the drive has the values), start and end pins with time pills, the chart-scrub dot and its readout,
// hover-to-time, a legend card and the GPS staleness badge. MapLibre, its worker and the PMTiles reader load as their own chunk when this mounts (loader.ts). Without WebGL it says
// the map is unavailable and leaves the rest of the page standing.
import { useEffect, useMemo, useRef } from "react";
import type { Map as MLMap, Marker } from "maplibre-gl";
import { ensureConfig, tilesPMTilesURL, valhallaBase } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { loadMapLibre } from "./loader";
import { webglAvailable } from "./webgl";
import { styleFor } from "./base";
import { MapUnavailable, TILES_UNAVAILABLE_TEXT } from "./Unavailable";
import { BASEMAP_ATTRIBUTION, type MapFlavor } from "./style";
import { boundsOf, firstLabelLayer, setDriveGradient, setDriveRoute, type LatLng } from "./route";
import { snapToRoads, type SnapPoint } from "./snap";
import { displayTimeZone } from "../lib/format";
import { localUnits } from "../lib/preferences";
import { cumulativeMiles, formatValue, gradientStops, percentileRange, RAMPS, seriesFor, type Mode, type Sample } from "./metrics";
import { placePills, setPills } from "./pills";
import { watchDrawn } from "./drawn";
import { cursorElement, flavorControl, foldCredits, modeLegend, pinElement, readoutElement, stalenessBadge, tilesNotice } from "./controls";

// alt (metres), soc (battery percent) and odo (odometer, miles) feed the Elevation and Efficiency modes; without them those modes are hidden.
type Point = { lat: number; lon: number; t?: number; s?: number; alt?: number; soc?: number; odo?: number };

export function DriveMap({
  points,
  start,
  end,
  height = 320,
  cursorTime,
  onCursorChange,
  fixAgeSeconds,
  packKWh,
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
  // The pack's size in kWh, for the Efficiency mode (battery change against distance); without it that mode is hidden.
  packKWh?: number;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MLMap | null>(null);
  // The timestamped samples (with their index into the drive's samples), and the readout text for sample i in the active mode.
  const timedRef = useRef<(Required<Pick<Point, "lat" | "lon" | "t">> & { i: number })[]>([]);
  const readoutRef = useRef<((i: number) => string) | null>(null);
  const readoutMarkerRef = useRef<Marker | null>(null);
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
      el.setAttribute("data-flavor", flavor);
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
      map.addControl(new ml.AttributionControl({ compact: true, customAttribution: BASEMAP_ATTRIBUTION }), "bottom-right");
      // The credits (the terrain line is long) start folded to the (i), at any width: unfolded they cover the legend and the route (map.css).
      foldCredits(el);
      if (!tiles) map.addControl(tilesNotice(TILES_UNAVAILABLE_TEXT), "bottom-left");

      // The D/L picker: a temporary override of the basemap that the app theme resets.
      let styleReady = false;
      const picker = flavorControl(flavor, (f) => setFlavor(f));
      const setFlavor = (f: MapFlavor) => {
        if (f === flavor) return;
        flavor = f;
        el.setAttribute("data-flavor", f);
        styleReady = false;
        picker.refresh(f);
        map.setStyle(styleFor(f));
      };
      // ---- the colour modes: which the drive supports, the active one, and the gradient it paints
      const samples: Sample[] = valid.map((p) => ({ lat: p.lat, lon: p.lon, t: p.t, s: p.s, alt: p.alt, soc: p.soc, odo: p.odo }));
      const { series, modes } = seriesFor(samples, packKWh);
      const dist = localUnits().distanceUnit;
      let mode: Mode = modes[0] ?? "speed";
      // The track the gradient runs along, and for each of its vertices the index of the sample whose value colours it.
      let track: LatLng[] = latlngs;
      let trackIdx: number[] = [...(startPt ? [0] : []), ...valid.map((_, i) => i), ...(endPt && !sameSpot ? [Math.max(0, valid.length - 1)] : [])];
      let trackCum = cumulativeMiles(track.map(([lat, lon]) => ({ lat, lon })));
      let routedGaps: LatLng[][] = [];
      let gaps: { from: LatLng; to: LatLng }[] = [];
      const gradient = () => {
        const vals = series[mode];
        if (!vals) {
          const c = RAMPS[flavor].speed[1];
          return { stops: [0, c, 1, c] as (number | string)[], lo: 0, hi: 0, ramp: RAMPS[flavor].speed };
        }
        const [lo, hi] = percentileRange(vals);
        const ramp = RAMPS[flavor][mode];
        return { stops: gradientStops(trackCum, trackIdx.map((i) => vals[i]), lo, hi, ramp), lo, hi, ramp };
      };
      const legend = modes.length > 0
        ? modeLegend(modes, (m) => { mode = m; applyMode(); }, { mode, flavor, ramp: RAMPS[flavor][mode], lo: "", hi: "" })
        : undefined;
      const refreshLegend = () => {
        const g = gradient();
        legend?.update({ mode, flavor, ramp: g.ramp, lo: formatValue(mode, g.lo, dist), hi: formatValue(mode, g.hi, dist) });
      };
      const applyMode = () => {
        refreshLegend();
        if (styleReady) setDriveGradient(map, gradient().stops);
      };
      map.addControl(picker, "top-right");
      cleanups.push(onAppMapFlavor((f) => { setFlavor(f); }));
      if (legend) map.addControl(legend, "bottom-left");
      if (typeof fixAgeSeconds === "number" && Number.isFinite(fixAgeSeconds) && fixAgeSeconds >= 5 * 60) map.addControl(stalenessBadge(fixAgeSeconds), "top-right");

      // What to keep framed: the raw trace first, the snapped route once it arrives; every resize refits to it.
      let fitBox = hasSpread ? boundsOf(latlngs) : null;

      // ---- the Start / End pills: map symbols (so the basemap's labels yield to them), on the side of each pin with more room
      const timeOf = (t: number | undefined) =>
        Number.isFinite(t) ? new Date((t as number) * 1000).toLocaleTimeString([], { hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() }) : "";
      const firstT = valid.find((p) => Number.isFinite(p.t))?.t;
      const lastT = [...valid].reverse().find((p) => Number.isFinite(p.t))?.t;
      let pillKey = "";
      const updatePills = (force = false) => {
        if (!styleReady || !lineStart || !(Number.isFinite(firstT) || Number.isFinite(lastT))) return;
        const sLL: [number, number] = [lineStart.lon, lineStart.lat];
        const eLL: [number, number] | null = lineEnd && !sameSpot ? [lineEnd.lon, lineEnd.lat] : null;
        const sides = placePills(map, sLL, eLL);
        const key = JSON.stringify([sides, flavor, firstT, lastT]);
        if (!force && key === pillKey) return;
        pillKey = key;
        const specs = [{ id: "start", text: `${sameSpot ? "Start" : "Start"} ${timeOf(firstT)}`, lngLat: sLL, ...sides.start }];
        if (eLL) specs.push({ id: "end", text: `End ${timeOf(lastT)}`, lngLat: eLL, ...sides.end });
        setPills(map, specs, flavor);
      };
      const fit = () => {
        if (!fitBox || !(el.clientWidth > 0 && el.clientHeight > 0)) return;
        // Keep the route's end clear of the legend card: its height, its lift above the credits, and room for the pin.
        const card = el.querySelector<HTMLElement>(".rivolt-legend");
        const lift = card ? card.offsetHeight + (parseFloat(getComputedStyle(card).marginBottom) || 0) + 28 : 0;
        const bottom = Math.max(el.clientWidth < 520 ? 64 : 44, lift);
        map.fitBounds(fitBox, { padding: { top: 44, left: 20, right: 72, bottom }, animate: false, maxZoom: 17 });
        updatePills();
      };

      // The style (re)loads on creation and on every flavour change; the route layers and pills go back each time.
      // Not map.isStyleLoaded(): that is false while any source is loading a tile, which dropped route updates made in that time.
      const draw = () => {
        const g = gradient();
        setDriveRoute(map, { track: hasSpread ? track : [], stops: g.stops, routedGaps, gaps }, flavor, firstLabelLayer(map));
        refreshLegend();
        updatePills(true);
        drawn.markDrawn();
      };
      const drawn = watchDrawn(map, el, "rivolt-track-main");
      cleanups.push(drawn.stop);
      map.on("style.load", () => { styleReady = true; draw(); });

      if (!hasSpread && latlngs.length > 0) map.jumpTo({ center: [latlngs[0][1], latlngs[0][0]], zoom: 16 });

      const markers: Marker[] = [];
      if (lineStart) markers.push(new ml.Marker({ element: pinElement("#10b981", sameSpot ? "Start / End" : "Start") }).setLngLat([lineStart.lon, lineStart.lat]).addTo(map));
      if (lineEnd && !sameSpot) markers.push(new ml.Marker({ element: pinElement("#f43f5e", "End") }).setLngLat([lineEnd.lon, lineEnd.lat]).addTo(map));
      cleanups.push(() => markers.forEach((m) => m.remove()));

      // Hover to time: the nearest timestamped sample within 28 px of the pointer.
      const timed = valid.flatMap((p, i) => (Number.isFinite(p.t) ? [{ lat: p.lat, lon: p.lon, t: p.t as number, i }] : []));
      timedRef.current = timed;
      // The readout beside the scrub dot: the time and the active mode's value at that sample.
      readoutRef.current = (i) => {
        const v = series[mode]?.[i];
        const when = timeOf(valid[i]?.t);
        return [when, v != null ? formatValue(mode, v, dist) : ""].filter(Boolean).join(" · ");
      };
      cleanups.push(() => { readoutRef.current = null; });
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
      const tracePoints: SnapPoint[] = [
        ...(startPt ? [{ ...startPt, t: synthT(firstT, -60) }] : []),
        ...valid,
        ...(endPt && !sameSpot ? [{ ...endPt, t: synthT(lastT, 60) }] : []),
      ];
      if (valhallaBase() !== "" && hasSpread) {
        snapToRoads(tracePoints, ac.signal).then((plan) => {
          if (ac.signal.aborted || mapRef.current !== map || !plan || plan.segments.length === 0) return;
          const all: LatLng[] = [];
          const t2: LatLng[] = [];
          const idx: number[] = [];
          for (const { coords: matched } of plan.segments) {
            for (const [lat, lon] of matched) {
              t2.push([lat, lon]);
              idx.push(nearestIndex(valid, lat, lon));
            }
            all.push(...matched);
          }
          for (const r of plan.routedGaps) all.push(...r);
          if (t2.length >= 2) {
            track = t2;
            trackIdx = idx;
            trackCum = cumulativeMiles(track.map(([lat, lon]) => ({ lat, lon })));
          }
          routedGaps = plan.routedGaps;
          gaps = plan.gaps;
          if (styleReady) draw();
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
        readoutMarkerRef.current?.remove();
        readoutMarkerRef.current = null;
        map.remove();
        mapRef.current = null;
      });
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));

    return () => { for (const c of cleanups.reverse()) c(); };
    // points is an array derived upstream; re-run only when its identity or the endpoints change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [supported, points, start?.lat, start?.lon, end?.lat, end?.lon, fixAgeSeconds, packKWh]);

  // The scrub dot and its readout follow cursorTime without rebuilding the map.
  useEffect(() => {
    const map = mapRef.current;
    const timed = timedRef.current;
    if (!map) return;
    if (cursorTime == null || !Number.isFinite(cursorTime) || timed.length === 0) {
      cursorRef.current?.remove();
      cursorRef.current = null;
      readoutMarkerRef.current?.remove();
      readoutMarkerRef.current = null;
      return;
    }
    let best = timed[0];
    let bestD = Math.abs(timed[0].t - cursorTime);
    for (let i = 1; i < timed.length; i++) {
      const d = Math.abs(timed[i].t - cursorTime);
      if (d < bestD) { bestD = d; best = timed[i]; }
    }
    const text = readoutRef.current?.(best.i) ?? "";
    if (cursorRef.current) {
      cursorRef.current.setLngLat([best.lon, best.lat]);
      readoutMarkerRef.current?.setLngLat([best.lon, best.lat]);
      const el = readoutMarkerRef.current?.getElement();
      if (el) el.textContent = text;
    } else {
      void loadMapLibre().then((ml) => {
        if (mapRef.current !== map || cursorRef.current) return;
        cursorRef.current = new ml.Marker({ element: cursorElement() }).setLngLat([best.lon, best.lat]).addTo(map);
        if (text) {
          readoutMarkerRef.current = new ml.Marker({ element: readoutElement(text), anchor: "bottom-left", offset: [10, -12] }).setLngLat([best.lon, best.lat]).addTo(map);
        }
      });
    }
  }, [cursorTime, points]);

  if (!supported) return <MapUnavailable height={height} />;
  return <div ref={ref} className="rounded-lg overflow-hidden border border-neutral-800" style={{ height }} />;
}

// nearestIndex is the index of the recorded sample nearest a snapped coordinate (flat-earth distance), so the snapped line takes that sample's value.
function nearestIndex(pts: { lat: number; lon: number }[], lat: number, lon: number): number {
  let bestI = 0;
  let bestD = Infinity;
  const cos = Math.cos((lat * Math.PI) / 180);
  for (let i = 0; i < pts.length; i++) {
    const dy = (pts[i].lat - lat) * 111111;
    const dx = (pts[i].lon - lon) * 111111 * cos;
    const d = dx * dx + dy * dy;
    if (d < bestD) { bestD = d; bestI = i; }
  }
  return bestI;
}
