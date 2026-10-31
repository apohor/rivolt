// TripRouteMap draws a planned trip on the self-hosted MapLibre basemap: a road-snapped line through every waypoint (a stored geometry first,
// then Valhalla, else straight legs), markers for the origin (green), each charging stop (amber) and the destination (rose), and a
// charger overlay along the route with add / preview popups. MapLibre loads as its own chunk when this mounts (loader.ts); without WebGL it
// says the map is unavailable and the itinerary beside it stands.
import { useEffect, useMemo, useRef, useState } from "react";
import type { GeoJSONSource, Map as MLMap, Marker, Popup } from "maplibre-gl";
import { inDemo } from "../lib/demo";
import { ensureConfig, tilesPMTilesURL, valhallaBase } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { findChargersAlongPath, HOTEL_FACILITY_TYPES } from "../lib/poi";
import type { POI } from "../lib/poi";
import type { HookupId } from "../plan/hookups";
import { campgroundOfPoi } from "../plan/stays";
import type { PlannedWaypoint, TripRoute } from "../lib/api";
import { styleFor } from "./base";
import { loadMapLibre } from "./loader";
import { webglAvailable } from "./webgl";
import { BASEMAP_ATTRIBUTION, type MapFlavor } from "./style";
import { ROUTE_UNIFORM, routeData, setRoute, type LatLng, type RouteFeature } from "./route";
import { foldCredits, tilesNotice } from "./controls";
import { MapUnavailable, TILES_UNAVAILABLE_TEXT } from "./Unavailable";
import {
  bookingDeepLink, campPopupHTML, chargerCircleStyle, chargerPopupHTML, chargerPreviewBodyHTML, hotelKw, valhallaMultiRoute, waypointTooltip,
  decodePolyline6, type PreviewResult,
} from "./tripPure";

type AddStop = (stop: { lat: number; lon: number; label: string }) => void;
type AddOvernight = (stop: { lat: number; lon: number; label: string; l2Kw?: number; hookup?: HookupId; campground?: boolean }) => void;
type PreviewStop = (stop: { lat: number; lon: number; label: string }) => Promise<PreviewResult | null>;

type Variant = "dcfc" | "l2" | "hotel" | "camp";
const CHARGERS_SOURCE = "rivolt-chargers";
const CHARGERS_LAYER = "rivolt-chargers-dots";
const VARIANTS: Variant[] = ["dcfc", "l2", "hotel", "camp"];

// The waypoint dot, plain or selected (the stops-table row is highlighted): the selected one is larger and ringed, its glow a touch target.
function dotElement(color: string, size: number, selected: boolean): HTMLElement {
  const el = document.createElement("span");
  el.className = selected ? "trip-route-dot-selected" : "trip-route-dot";
  const px = selected ? size + 6 : size;
  el.style.cssText = selected
    ? `display:block;width:${px}px;height:${px}px;border-radius:9999px;background:${color};border:3px solid #fafafa;box-shadow:0 0 0 4px ${color}55,0 0 12px ${color}99;`
    : `display:block;width:${px}px;height:${px}px;border-radius:9999px;background:${color};border:2px solid #0a0a0a;box-shadow:0 0 0 1px ${color}88;`;
  return el;
}

function chargerVariant(poi: POI): { variant: Variant; isHotel: boolean; camp: ReturnType<typeof campgroundOfPoi> } {
  const isHotel = !!(poi.facilityType && HOTEL_FACILITY_TYPES.has(poi.facilityType.toUpperCase()));
  const camp = campgroundOfPoi(poi);
  const variant: Variant = camp ? "camp" : isHotel ? "hotel" : poi.isDCFC !== false ? "dcfc" : "l2";
  return { variant, isHotel, camp };
}

function lineData(path: LatLng[]): ReturnType<typeof routeData> {
  const f: RouteFeature = { type: "Feature", geometry: { type: "LineString", coordinates: path.map(([la, lo]) => [lo, la] as [number, number]) }, properties: { kind: "speed", color: ROUTE_UNIFORM } };
  return routeData({ runs: path.length >= 2 ? [f] : [] });
}

function boundsOfPath(path: LatLng[]): [[number, number], [number, number]] {
  let w = Infinity, s = Infinity, e = -Infinity, n = -Infinity;
  for (const [la, lo] of path) {
    if (lo < w) w = lo;
    if (lo > e) e = lo;
    if (la < s) s = la;
    if (la > n) n = la;
  }
  return [[w, s], [e, n]];
}

export function TripRouteMap({
  route,
  height = 320,
  fill = false,
  onAddStop,
  onAddOvernight,
  onPreviewStop,
  selectedIdx,
  onSelectStop,
  departureAt,
}: {
  route: TripRoute;
  height?: number;
  // Fill the parent (the map-first layout): chips float over the map.
  fill?: boolean;
  onAddStop?: AddStop;
  // Hotel-L2 and campground popups get a dedicated "Add as overnight stop" button.
  onAddOvernight?: AddOvernight;
  // ISO 8601 departure datetime from the planner form, for the check-in date on hotel Booking.com links. Empty falls back to today.
  departureAt?: string;
  // When provided, the charger popup gains a "Preview impact" button that fires a hypothetical replan and shows the deltas before the user commits.
  onPreviewStop?: PreviewStop;
  // Bidirectional selection between the stops table and the map: the index into route.Waypoints of the highlighted waypoint, or null.
  selectedIdx?: number | null;
  onSelectStop?: (idx: number | null) => void;
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MLMap | null>(null);
  const [mapReady, setMapReady] = useState<MLMap | null>(null);
  // Waypoint markers by their index in route.Waypoints, so a table-row selection can restyle the right one.
  const wpMarkersRef = useRef<Map<number, { marker: Marker; color: string; size: number; wrap: HTMLElement }>>(new Map());
  const chargersRef = useRef<GeoJSON.FeatureCollection>({ type: "FeatureCollection", features: [] });
  const popupRef = useRef<Popup | null>(null);
  const pois = useRef<POI[]>([]);
  const departureAtRef = useRef(departureAt);
  departureAtRef.current = departureAt;
  const [routePath, setRoutePath] = useState<LatLng[]>([]);
  const [chargerFilter, setChargerFilter] = useState<"dcfc" | "l2" | "hotels" | "camp" | "all">("dcfc");
  const [campCount, setCampCount] = useState(0);
  const [chargerCount, setChargerCount] = useState<number | null>(null);
  const [chargersLoading, setChargersLoading] = useState(false);
  // No charger source (the signed-out demo, or a server without the archive): the chips would contradict a route that has charging stops.
  const [noChargerSource, setNoChargerSource] = useState(inDemo());
  // Refs so async handlers call the latest callbacks without being effect dependencies (which would re-mount the map every render).
  const onAddStopRef = useRef(onAddStop);
  useEffect(() => { onAddStopRef.current = onAddStop; }, [onAddStop]);
  const onAddOvernightRef = useRef(onAddOvernight);
  useEffect(() => { onAddOvernightRef.current = onAddOvernight; }, [onAddOvernight]);
  const onSelectStopRef = useRef(onSelectStop);
  useEffect(() => { onSelectStopRef.current = onSelectStop; }, [onSelectStop]);
  const onPreviewStopRef = useRef(onPreviewStop);
  useEffect(() => { onPreviewStopRef.current = onPreviewStop; }, [onPreviewStop]);

  // Effect 1: the map, the route and the waypoint markers. Re-runs only when the route changes.
  useEffect(() => {
    if (!supported || !ref.current) return;
    const el = ref.current;
    const wsWithIdx = route.Waypoints
      .map((w, i) => ({ w, i }))
      .filter(({ w }) => Number.isFinite(w.Latitude) && Number.isFinite(w.Longitude) && !(w.Latitude === 0 && w.Longitude === 0));
    const ws: PlannedWaypoint[] = wsWithIdx.map((x) => x.w);
    if (ws.length < 2) return;

    let alive = true;
    const ac = new AbortController();
    const cleanups: (() => void)[] = [() => { alive = false; ac.abort(); }];

    (async () => {
      const [ml] = await Promise.all([loadMapLibre(), ensureConfig()]);
      if (!alive || !el.isConnected) return;

      let flavor: MapFlavor = appMapFlavor();
      const tiles = tilesPMTilesURL().startsWith("/");
      const wpLatLngs: LatLng[] = ws.map((w) => [w.Latitude, w.Longitude]);
      const map = new ml.Map({
        container: el,
        style: styleFor(flavor),
        bounds: boundsOfPath(wpLatLngs),
        fitBoundsOptions: { padding: 24, maxZoom: 12, animate: false },
        attributionControl: false,
        dragRotate: false,
        pitchWithRotate: false,
        touchPitch: false,
        fadeDuration: 150,
      });
      mapRef.current = map;
      el.setAttribute("data-flavor", flavor);
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
      foldCredits(el);
      if (!tiles) map.addControl(tilesNotice(TILES_UNAVAILABLE_TEXT), "bottom-left");

      // In the fill layout the charger chips float over the top of the map: pad the fit so the route clears them.
      const narrow = window.innerWidth < 640;
      const padding = () =>
        fill ? { top: narrow ? 96 : 64, bottom: narrow ? 40 : 24, left: 24, right: 24 } : { top: 24, bottom: 24, left: 24, right: 24 };
      let fitPath: LatLng[] = wpLatLngs;
      let fitted = false;
      const fit = (maxZoom: number) => {
        if (!(el.clientWidth > 0 && el.clientHeight > 0)) { fitted = false; return; }
        map.fitBounds(boundsOfPath(fitPath), { padding: padding(), maxZoom, animate: false });
        fitted = true;
      };
      let fitZoom = 12;

      // The line (straight legs first), re-added after every style change.
      let data = lineData(wpLatLngs);
      map.on("style.load", () => {
        setRoute(map, data, flavor);
        // The charger overlay goes back too.
        addChargerLayer(map);
      });

      // Waypoint markers: click selects the stops-table row; hovering shows the tooltip.
      wpMarkersRef.current.clear();
      const tip = new ml.Popup({ closeButton: false, closeOnClick: false, offset: 12, className: "rivolt-tip", maxWidth: "260px" });
      for (let i = 0; i < ws.length; i++) {
        const w = ws[i];
        const origIdx = wsWithIdx[i].i;
        const isEnd = i === 0 || i === ws.length - 1;
        const color = i === 0 ? "#10b981" : i === ws.length - 1 ? "#f43f5e" : "#f59e0b";
        const size = isEnd ? 12 : 10;
        // The marker's own element keeps the library's classes and a fixed box (a steady anchor and a touch target); the dot inside swaps on selection.
        const element = document.createElement("div");
        element.style.cssText = "display:flex;align-items:center;justify-content:center;width:28px;height:28px;";
        element.appendChild(dotElement(color, size, false));
        element.setAttribute("role", "button");
        element.setAttribute("aria-label", i === 0 ? "Origin" : i === ws.length - 1 ? "Destination" : w.Name || "Charging stop");
        element.style.cursor = "pointer";
        const marker = new ml.Marker({ element }).setLngLat([w.Longitude, w.Latitude]).addTo(map);
        element.addEventListener("click", (ev) => { ev.stopPropagation(); onSelectStopRef.current?.(origIdx); });
        element.addEventListener("mouseenter", () => tip.setLngLat([w.Longitude, w.Latitude]).setHTML(waypointTooltip(w, i, ws.length)).addTo(map));
        element.addEventListener("mouseleave", () => tip.remove());
        wpMarkersRef.current.set(origIdx, { marker, color, size, wrap: element });
      }
      cleanups.push(() => { tip.remove(); wpMarkersRef.current.forEach((m) => m.marker.remove()); wpMarkersRef.current.clear(); });

      // The basemap follows the app theme live (Settings, or the OS under System).
      cleanups.push(onAppMapFlavor((f) => {
        if (f === flavor) return;
        flavor = f;
        el.setAttribute("data-flavor", f);
        map.setStyle(styleFor(f));
      }));

      // Containers sized after mount (a freshly revealed card, a lazy chunk): resize, and fit if it could not before.
      let roRaf = 0;
      const ro = new ResizeObserver(() => {
        if (roRaf) return;
        roRaf = requestAnimationFrame(() => {
          roRaf = 0;
          if (!alive || mapRef.current !== map) return;
          map.resize();
          if (!fitted) fit(fitZoom);
        });
      });
      ro.observe(el);
      cleanups.push(() => { ro.disconnect(); if (roRaf) cancelAnimationFrame(roRaf); });
      map.once("load", () => fit(fitZoom));
      cleanups.push(() => {
        popupRef.current?.remove();
        popupRef.current = null;
        map.remove();
        mapRef.current = null;
        setMapReady(null);
        setRoutePath([]);
        setChargerCount(null);
      });
      setMapReady(map);

      // The route: a stored geometry (the demo's preset trips) beats Valhalla, which beats straight legs.
      let path: LatLng[] = wpLatLngs;
      const stored = route.route_polyline ? decodePolyline6(route.route_polyline) : [];
      if (stored.length >= 2) {
        path = stored;
      } else if (!inDemo() && valhallaBase() !== "") {
        const snapped = await valhallaMultiRoute(ws, ac.signal);
        if (snapped && snapped.length >= 2 && !ac.signal.aborted) path = snapped;
      }
      if (!alive || mapRef.current !== map) return;
      if (path !== wpLatLngs) {
        data = lineData(path);
        if (map.isStyleLoaded()) setRoute(map, data, flavor);
        fitPath = path;
        fitZoom = 13;
        fit(fitZoom);
      }
      setRoutePath(path);
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));

    return () => { for (const c of cleanups.reverse()) c(); };
  }, [supported, route, fill]);

  // The charger overlay is one GeoJSON source drawn as circles on the map's canvas: hundreds of chargers cost no DOM nodes.
  function addChargerLayer(map: MLMap) {
    if (!map.getSource(CHARGERS_SOURCE)) map.addSource(CHARGERS_SOURCE, { type: "geojson", data: chargersRef.current as never });
    if (map.getLayer(CHARGERS_LAYER)) return;
    const pick = <T,>(f: (v: Variant) => T) => VARIANTS.flatMap((v) => [v, f(v)]);
    map.addLayer({
      id: CHARGERS_LAYER,
      type: "circle",
      source: CHARGERS_SOURCE,
      paint: {
        "circle-radius": ["match", ["get", "variant"], ...pick((v) => chargerCircleStyle(v).radius), 4] as never,
        "circle-color": ["match", ["get", "variant"], ...pick((v) => chargerCircleStyle(v).fill), "#06b6d4"] as never,
        "circle-stroke-color": ["match", ["get", "variant"], ...pick((v) => chargerCircleStyle(v).stroke), "#0a0a0a"] as never,
        "circle-stroke-width": ["match", ["get", "variant"], ...pick((v) => chargerCircleStyle(v).weight), 1.5] as never,
        "circle-opacity": 0.92,
      },
    });
  }

  // Effect: wire the charger layer's click → popup once per map.
  useEffect(() => {
    const map = mapReady;
    if (!map) return;
    let off = () => {};
    void loadMapLibre().then((ml) => {
      if (mapRef.current !== map) return;
      const onClick = (e: { features?: { properties?: Record<string, unknown> }[] }) => {
        const idx = Number(e.features?.[0]?.properties?.idx);
        const poi = pois.current[idx];
        if (!poi) return;
        popupRef.current?.remove();
        const { isHotel, camp } = chargerVariant(poi);
        const html = camp
          ? campPopupHTML(poi, camp, !!onAddOvernightRef.current)
          : chargerPopupHTML(poi, !!onAddStopRef.current, !!onPreviewStopRef.current, isHotel ? bookingDeepLink(poi, departureAtRef.current) : "", isHotel && !!onAddOvernightRef.current);
        const popup = new ml.Popup({ closeButton: true, offset: 8, maxWidth: "280px", className: "rivolt-popup" }).setLngLat([poi.lon, poi.lat]).setHTML(html).addTo(map);
        popupRef.current = popup;
        wirePopup(popup, poi, camp);
      };
      const enter = () => { map.getCanvas().style.cursor = "pointer"; };
      const leave = () => { map.getCanvas().style.cursor = ""; };
      map.on("click", CHARGERS_LAYER, onClick as never);
      map.on("mouseenter", CHARGERS_LAYER, enter);
      map.on("mouseleave", CHARGERS_LAYER, leave);
      off = () => { map.off("click", CHARGERS_LAYER, onClick as never); map.off("mouseenter", CHARGERS_LAYER, enter); map.off("mouseleave", CHARGERS_LAYER, leave); };
    });
    return () => off();
  }, [mapReady]);

  // The popup's buttons (add, overnight, preview, confirm, cancel) are plain HTML; wire them to the planner's callbacks.
  function wirePopup(popup: Popup, poi: POI, camp: ReturnType<typeof campgroundOfPoi>) {
    const el = popup.getElement();
    if (!el) return;
    const label = poi.name || poi.network || `${poi.lat.toFixed(4)}, ${poi.lon.toFixed(4)}`;
    el.querySelector<HTMLButtonElement>(".charger-add-btn")?.addEventListener("click", () => {
      onAddStopRef.current?.({ lat: poi.lat, lon: poi.lon, label });
      popup.remove();
    });
    el.querySelector<HTMLButtonElement>(".charger-overnight-btn")?.addEventListener("click", () => {
      if (camp) onAddOvernightRef.current?.({ lat: poi.lat, lon: poi.lon, label: `${label} (campground)`, l2Kw: camp.kw, hookup: camp.hookup, campground: true });
      else onAddOvernightRef.current?.({ lat: poi.lat, lon: poi.lon, label, l2Kw: hotelKw(poi) });
      popup.remove();
    });
    const previewBtn = el.querySelector<HTMLButtonElement>(".charger-preview-btn");
    if (previewBtn && onPreviewStopRef.current) {
      previewBtn.addEventListener("click", async () => {
        previewBtn.disabled = true;
        previewBtn.textContent = "Computing impact…";
        try {
          const r = await onPreviewStopRef.current!({ lat: poi.lat, lon: poi.lon, label });
          const body = el.querySelector<HTMLDivElement>(".charger-popup-body");
          if (!body) return;
          if (!r) { previewBtn.textContent = "Preview unavailable"; return; }
          body.innerHTML = chargerPreviewBodyHTML(poi, r);
          body.querySelector<HTMLButtonElement>(".charger-confirm-btn")?.addEventListener("click", () => {
            onAddStopRef.current?.({ lat: poi.lat, lon: poi.lon, label });
            popup.remove();
          });
          body.querySelector<HTMLButtonElement>(".charger-cancel-btn")?.addEventListener("click", () => popup.remove());
        } catch {
          previewBtn.textContent = "Preview failed";
        }
      });
    }
  }

  // Effect: external selection. A table-row click makes the matching marker larger and ringed and pans onto it; the previous one reverts.
  useEffect(() => {
    const markers = wpMarkersRef.current;
    if (markers.size === 0) return;
    for (const [idx, m] of markers.entries()) {
      const selected = selectedIdx === idx;
      m.wrap.replaceChildren(dotElement(m.color, m.size, selected));
      m.wrap.style.zIndex = selected ? "1000" : "";
    }
    const map = mapRef.current;
    if (map && typeof selectedIdx === "number") {
      const m = markers.get(selectedIdx);
      if (m) map.easeTo({ center: m.marker.getLngLat(), duration: 300 });
    }
  }, [selectedIdx, mapReady]);

  // Effect 2: the charger overlay. Re-runs on filter or route-path change; only the charger source is rewritten, so a filter swap is instant.
  useEffect(() => {
    const map = mapReady;
    if (routePath.length < 2 || !map) return;
    const ac = new AbortController();
    setChargersLoading(true);
    void (async () => {
      const found = await findChargersAlongPath(routePath, chargerFilter);
      if (ac.signal.aborted || mapRef.current !== map) return;
      setNoChargerSource(found === null);
      const chargers = found ?? [];
      setChargerCount(chargers.length);
      setChargersLoading(false);
      pois.current = chargers;
      chargersRef.current = {
        type: "FeatureCollection",
        features: chargers.map((poi, idx) => ({
          type: "Feature" as const,
          geometry: { type: "Point" as const, coordinates: [poi.lon, poi.lat] },
          properties: { idx, variant: chargerVariant(poi).variant },
        })),
      };
      const apply = () => {
        const src = map.getSource(CHARGERS_SOURCE) as GeoJSONSource | undefined;
        if (src) src.setData(chargersRef.current as never);
        else addChargerLayer(map);
      };
      if (map.isStyleLoaded()) apply();
      else map.once("styledata", apply);
    })();
    return () => {
      ac.abort();
      setChargersLoading(false);
    };
  }, [routePath, chargerFilter, mapReady]);

  // Is there any campground with a hookup along the route? Decides whether the chip shows at all.
  useEffect(() => {
    if (routePath.length < 2) return;
    let live = true;
    void findChargersAlongPath(routePath, "camp")
      .then((found) => { if (live) setCampCount((found ?? []).filter((p) => campgroundOfPoi(p)).length); })
      .catch(() => { if (live) setCampCount(0); });
    return () => { live = false; };
  }, [routePath]);

  const filterLabels: Record<typeof chargerFilter, string> = {
    dcfc: "Fast charging",
    l2: "Level 2",
    hotels: "Hotels with overnight charging",
    camp: "Campgrounds",
    all: "All",
  };

  if (!supported) {
    return (
      <div className={fill ? "h-full" : undefined}>
        <MapUnavailable height={fill ? 240 : height} note="The itinerary beside it is unaffected." />
      </div>
    );
  }

  return (
    <div className={fill ? "relative h-full" : undefined}>
      {!noChargerSource && (
        <div className={fill ? "absolute left-14 top-3 z-[500] flex flex-wrap items-center gap-1 rounded-lg bg-neutral-950/80 px-2 py-1.5 backdrop-blur-sm" : "flex flex-wrap items-center gap-1 mb-1"} data-plan-chips={fill ? "" : undefined} data-map-chips>
          <span className="whitespace-nowrap text-xs text-neutral-500 mr-1">Chargers:</span>
          {(["dcfc", "l2", "hotels", "camp", "all"] as const).filter((f) => f !== "camp" || campCount > 0 || chargerFilter === "camp").map((f) => (
            <button
              key={f}
              onClick={() => setChargerFilter(f)}
              className={`whitespace-nowrap px-2 py-0.5 text-xs rounded transition-colors ${
                chargerFilter === f ? "bg-cyan-800 text-cyan-100" : "bg-neutral-800 text-neutral-400 hover:text-neutral-200"
              }`}
            >
              {filterLabels[f]}
            </button>
          ))}
          {!chargersLoading && chargerCount === 0 ? (
            <span className="basis-full text-xs text-neutral-400">No chargers in this area at this zoom. Zoom out or move the map.</span>
          ) : (
            <span className="whitespace-nowrap text-xs text-neutral-500 ml-1">{chargersLoading ? "…" : chargerCount != null ? `${chargerCount} found` : ""}</span>
          )}
          {chargerFilter === "all" && (
            <span className="whitespace-nowrap text-xs text-neutral-600 ml-1">
              <span style={{ color: "#06b6d4" }}>●</span> Fast charging &nbsp;
              <span style={{ color: "#a3e635" }}>●</span> Level 2 &nbsp;
              <span style={{ color: "#a78bfa" }}>●</span> Hotel, overnight charging &nbsp;
              {campCount > 0 && (<><span style={{ color: "#22c55e" }}>●</span> Campground, electric hookup</>)}
            </span>
          )}
          {chargerFilter === "camp" && (
            <span className="text-xs text-neutral-600 ml-1"><span style={{ color: "#22c55e" }}>●</span> Campground, electric hookup</span>
          )}
          {chargerFilter === "hotels" && (
            <span className="text-xs text-neutral-600 ml-1"><span style={{ color: "#a78bfa" }}>●</span> Hotel, overnight charging</span>
          )}
        </div>
      )}
      <div
        ref={ref}
        className={fill ? "h-full overflow-hidden" : "rounded-lg overflow-hidden border border-neutral-800"}
        style={fill ? undefined : { height }}
      />
    </div>
  );
}
