// TripRouteMap draws a planned trip on the self-hosted MapLibre basemap: a road-snapped line through every waypoint (a stored geometry first,
// then Valhalla, else straight legs), markers for the origin (green), each charging stop (amber) and the destination (rose), and a
// charger overlay along the route with add / preview popups. MapLibre loads as its own chunk when this mounts (loader.ts); without WebGL it
// says the map is unavailable and the itinerary beside it stands.
import { fetchRadarFrame, hideRadar, RADAR_DENIED, radarAsOf, radarEnabled, radarSpoken, RADAR_REFRESH_MS, showRadar as addRadar } from "./radar";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { GeoJSONSource, Map as MLMap, Marker, Popup } from "maplibre-gl";
import { inDemo } from "../lib/demo";
import { ensureConfig, tilesPMTilesURL, valhallaBase } from "../lib/config";
import { appMapFlavor, onAppMapFlavor } from "../lib/mapTheme";
import { findChargersOfNetworks, findLayersAlongPath, HOTEL_FACILITY_TYPES } from "../lib/poi";
import { layerFiltersOf, usePlannerPrefs } from "../lib/plannerPrefs";
import type { LayerKind, POI } from "../lib/poi";
import type { HookupId } from "../plan/hookups";
import { campgroundOfPoi } from "../plan/stays";
import { backend, type PlannedWaypoint, type TripRoute } from "../lib/api";
import { addRequest, whatIfFailureLine, whatIfLines, WHAT_IF_NOTICE, WHAT_IF_ROUGH } from "../plan/whatIf";
import { styleFor } from "./base";
import { loadMapLibre } from "./loader";
import { webglAvailable } from "./webgl";
import { BASEMAP_ATTRIBUTION, type MapFlavor } from "./style";
import { firstLabelLayer, setDriveRoute, type LatLng } from "./route";
import { RAMPS } from "./metrics";
import { chargingStopCount, isChargingWaypoint } from "../plan/waypoints";
import { measurePill, pillAwayFromLine, pillBox, pillSide, placeStopPill, setPills, type Box, type PillSpec } from "./pills";
import { watchDrawn } from "./drawn";
import { localUnits, usePreferences } from "../lib/preferences";
import { MAP_CAPTION_PX } from "../lib/mapCaption";
import { chargerLabel, type ChargerKind } from "../lib/chargerLabels";
import { ChargerTypeInfo } from "../components/ChargerTypeInfo";
import { foldCredits, tripCard, tilesNotice } from "./controls";
import { drawLegBadges, type BadgesHandle, type LegWeatherInfo } from "./legBadges";
import { legWeatherTags } from "../plan/legWeather";
import { MapUnavailable, TILES_UNAVAILABLE_TEXT } from "./Unavailable";
import {
  bookingDeepLink, campPopupHTML, chargerCircleStyle, chargerPopupHTML, chargerPreviewBodyHTML, DROPPED_PIN_LABEL, droppedPinPopupHTML, hotelKw, offRouteLine, sameSpot, valhallaMultiRoute, waypointTooltip,
  decodePolyline6, tripCardText, type PreviewResult,
} from "./tripPure";

type AddStop = (stop: { lat: number; lon: number; label: string }) => void;
type AddOvernight = (stop: { lat: number; lon: number; label: string; l2Kw?: number; hookup?: HookupId; campground?: boolean }) => void;
type PreviewStop = (stop: { lat: number; lon: number; label: string }) => Promise<PreviewResult | null>;

type Variant = "dcfc" | "l2" | "hotel" | "camp";
const CHARGERS_SOURCE = "rivolt-chargers";
const CHARGERS_LAYER = "rivolt-chargers-dots";
const VARIANTS: Variant[] = ["dcfc", "l2", "hotel", "camp"];

// The waypoint dot, plain or selected (the stops-table row is highlighted): the selected one is larger and ringed, its glow a touch target.
function dotElement(color: string, size: number, selected: boolean, bolt = false): HTMLElement {
  const el = document.createElement("span");
  if (bolt) {
    // A charging stop: an amber disc with a bolt, as in the mockup; the selected one is ringed and larger.
    const px = selected ? 26 : 22;
    el.className = selected ? "trip-route-dot-selected" : "trip-route-dot";
    el.textContent = "⚡";
    el.style.cssText =
      `display:inline-flex;align-items:center;justify-content:center;width:${px}px;height:${px}px;border-radius:9999px;background:${color};color:#0a0a0a;` +
      `font:700 ${selected ? 15 : 13}px/1 ui-sans-serif,system-ui;border:${selected ? 3 : 2}px solid ${selected ? "#fafafa" : "#0a0a0a"};box-shadow:0 0 0 ${selected ? 4 : 2}px ${color}55;`;
    return el;
  }
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

// The slugs of the networks marked "Prefer in planner" in Settings (read once; the signed-out demo has none). A failed read means no chip.
function usePreferredSlugs(): string[] {
  const [slugs, setSlugs] = useState<string[]>([]);
  useEffect(() => {
    if (inDemo()) return;
    let live = true;
    void Promise.resolve()
      .then(() => backend.getChargingNetworks())
      .then((rows) => { if (live) setSlugs((rows ?? []).filter((n) => n.preferred && n.slug).map((n) => n.slug as string)); })
      .catch(() => {});
    return () => { live = false; };
  }, []);
  return slugs;
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
  dayLabel,
  startsAtOrigin = true,
  originLabel,
  destLabel,
  legWeather,
  sheetSnap,
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
  // "Day 2" on a multi-day plan's map: the card says which day it is.
  dayLabel?: string;
  // False for a line that does not start at the trip's origin (a later day, a re-plan from the truck): "hide chargers near the start" then does not
  // apply, because a day that starts at an overnight stop did not leave charged.
  startsAtOrigin?: boolean;
  // The names on the origin and destination pills; without them the waypoints' own names.
  originLabel?: string;
  destLabel?: string;
  // The weather on each leg (leg i runs from waypoint i to i+1; null where there is none): drawn as a badge on the route, with a "Weather" chip.
  legWeather?: (LegWeatherInfo | null)[];
  // The phone's bottom sheet height (peek, half, full): once it settles, the map's fit and its credits follow the sheet's edge.
  sheetSnap?: "peek" | "half" | "full";
}) {
  const supported = useMemo(() => webglAvailable(), []);
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<MLMap | null>(null);
  const [mapReady, setMapReady] = useState<MLMap | null>(null);
  // Waypoint markers by their index in route.Waypoints, so a table-row selection can restyle the right one.
  const selectedIdxRef = useRef(selectedIdx);
  selectedIdxRef.current = selectedIdx;
  const wpMarkersRef = useRef<Map<number, { marker: Marker; color: string; size: number; wrap: HTMLElement; bolt: boolean }>>(new Map());
  const chargersRef = useRef<GeoJSON.FeatureCollection>({ type: "FeatureCollection", features: [] });
  const popupRef = useRef<Popup | null>(null);
  // True between a style's "style.load" and the next setStyle. Not map.isStyleLoaded(): that is false while any source is still loading a
  // tile, so a route update that waited for it was dropped whenever tiles were in flight (the line missing after switching plans).
  const styleReadyRef = useRef(false);
  const pois = useRef<POI[]>([]);
  const departureAtRef = useRef(departureAt);
  departureAtRef.current = departureAt;
  const [routePath, setRoutePath] = useState<LatLng[]>([]);
  // The Trip planner settings: what the map starts with (layers, preferred only, weather) and how the charger query is shaped. The map waits for
  // them so it does not ask once with the defaults and again with the user's choices.
  const { prefs, settled: prefsSettled } = usePlannerPrefs();
  const filters = useMemo(
    () => ({ ...layerFiltersOf(prefs), ...(startsAtOrigin ? {} : { start_exclude_mi: 0 as const }) }),
    [prefs.corridor, prefs.start_exclude_mi, prefs.min_fast_kw, startsAtOrigin], // eslint-disable-line react-hooks/exhaustive-deps
  );
  const filtersKey = `${filters.preset}|${filters.start_exclude_mi}|${filters.min_fast_kw}`;
  // A switch the person has already flipped on this map is theirs; the settings only supply the starting state of the others.
  const touchedRef = useRef({ layers: false, preferred: false, weather: false, radar: false });
  // The layers shown, each an independent switch: tap a lit chip to turn it off. Fast is on unless the settings say otherwise; with every chip off
  // nothing is drawn and nothing is requested.
  const [layers, setLayers] = useState<ReadonlySet<LayerKind>>(() => new Set<LayerKind>(["dcfc"]));
  const toggleLayer = (k: LayerKind) => { touchedRef.current.layers = true; setLayers((cur) => { const next = new Set(cur); if (next.has(k)) next.delete(k); else next.add(k); return next; }); };
  const layersKey = (["dcfc", "l2", "hotels", "camp"] as const).filter((k) => layers.has(k)).join(",");
  const fastOn = layers.has("dcfc");
  const [campCount, setCampCount] = useState(0);
  // "Preferred only": just the fast chargers of the networks marked "Prefer in planner" in Settings (the same list). It narrows the map; the planner
  // still only favours them.
  const [preferredOnly, setPreferredOnlyState] = useState(false);
  const setPreferredOnly = (v: boolean) => { touchedRef.current.preferred = true; setPreferredOnlyState(v); };
  const preferredSlugs = usePreferredSlugs();
  const preferredKey = preferredSlugs.join(",");
  const onlyPreferred = preferredOnly && preferredSlugs.length > 0 && fastOn;
  const [chargerCount, setChargerCount] = useState<number | null>(null);
  const [chargerTotal, setChargerTotal] = useState<number | null>(null);
  // A layer request that failed for a reason other than a chip toggle cancelling it: said in the chip row, so an empty map is not mistaken for no stations.
  const [layerError, setLayerError] = useState(false);
  // "Try again" bumps this to re-run the same layer request (the effect below depends on it).
  const [retry, setRetry] = useState(0);
  const [chargersLoading, setChargersLoading] = useState(false);
  // No charger source (the signed-out demo, or a server without the archive): the chips would contradict a route that has charging stops.
  const [noChargerSource, setNoChargerSource] = useState(inDemo());
  const hasWeather = !!legWeather?.some((l) => l);
  // The caller builds the array afresh each render; redraw only when its content changes.
  const weatherKey = JSON.stringify(legWeather ?? null);
  // Where the pins are on the map right now (the leg badges keep clear of them), and the pills' placement, which the badges call again once they move.
  const pillTakenRef = useRef<Box[]>([]);
  // What floats over the map's top and bottom (the charger chips, the radar strip, the route summary card), in the map's own px. One that is not drawn
  // (the trip card under 200 px) is no obstacle. getClientRects, not offsetParent: the strip on a phone is position: fixed and has no offsetParent.
  const overlayBoxes = (container: HTMLElement): Box[] => {
    const here = container.getBoundingClientRect();
    return Array.from(container.parentElement?.querySelectorAll<HTMLElement>("[data-map-chips], [data-radar-label], .rivolt-trip-card") ?? []).filter((o) => o.getClientRects().length > 0).map((o) => {
      const r = o.getBoundingClientRect();
      return { x0: r.left - here.left, y0: r.top - here.top, x1: r.right - here.left, y1: r.bottom - here.top };
    });
  };
  const updatePillsRef = useRef<((force?: boolean) => void) | null>(null);
  const legWeatherRef = useRef(legWeather);
  legWeatherRef.current = legWeather;
  // The starting state comes from the Trip planner settings (on unless turned off there); the chip changes this map only.
  const [showWeather, setShowWeather] = useState(true);
  const [weatherNote, setWeatherNote] = useState("");
  const toggleWeather = () => {
    const next = !showWeather;
    touchedRef.current.weather = true;
    setShowWeather(next);
    setWeatherNote(next ? "Weather on" : "Weather off");
    window.setTimeout(() => setWeatherNote(""), 1600);
  };
  // Live radar: starts as the Trip planner settings say (off by default); hidden entirely when the server has the radar flag off.
  // A 401/403 on the frame (radar is not served to this visitor) hides the button and says nothing, as if the server had no radar.
  const [radarDenied, setRadarDenied] = useState(false);
  const radarOn = radarEnabled() && !radarDenied;
  // The map's fit reads the radar row's room through this, so a refused radar stops reserving it.
  const radarOnRef = useRef(radarOn);
  radarOnRef.current = radarOn;
  // Reframes the route with the current padding (null until the map is up); called once the radar row goes away.
  const reframeRef = useRef<(() => void) | null>(null);
  useEffect(() => { if (radarDenied) reframeRef.current?.(); }, [radarDenied]);
  const [showRadar, setShowRadar] = useState(false);
  const [radarLabel, setRadarLabel] = useState("");
  const [radarSpokenLabel, setRadarSpokenLabel] = useState("");
  const toggleRadar = () => {
    touchedRef.current.radar = true;
    setShowRadar(!showRadar);
  };
  // Effect: while the radar button is on, draw the newest frame under the route and look for a newer one every few minutes.
  useEffect(() => {
    const map = mapReady;
    if (!map || !radarOn || !showRadar) return;
    let alive = true;
    const ac = new AbortController();
    const refresh = async () => {
      const f = await fetchRadarFrame(ac.signal);
      if (!alive) return;
      if (f === RADAR_DENIED) {
        hideRadar(map);
        setRadarDenied(true);
        return;
      }
      if (!f) { // feed down or stale: show nothing rather than an old picture
        hideRadar(map);
        setRadarLabel("Radar unavailable");
        setRadarSpokenLabel("Radar unavailable");
        return;
      }
      drawRadar(map, f.frameUnix);
      setRadarLabel(radarAsOf(f.frameUnix));
      setRadarSpokenLabel(radarSpoken(f.frameUnix));
    };
    // styleReadyRef, not isStyleLoaded(): that is false while any tile is in flight, and the one style.load had already fired, so the first plan never drew.
    const drawRadar = (m: MLMap, t: number) => { if (styleReadyRef.current) addRadar(m, t); else m.once("style.load", () => alive && addRadar(m, t)); };
    void refresh();
    const timer = window.setInterval(() => void refresh(), RADAR_REFRESH_MS);
    return () => { alive = false; ac.abort(); window.clearInterval(timer); hideRadar(map); setRadarLabel(""); setRadarSpokenLabel(""); };
  }, [mapReady, radarOn, showRadar]);
  // Effect: once the settings are in, they set the starting state of every switch the person has not flipped yet.
  const [started, setStarted] = useState(false);
  const startedRef = useRef(false);
  useEffect(() => {
    if (!prefsSettled || startedRef.current) return;
    startedRef.current = true;
    setStarted(true);
    if (!touchedRef.current.layers) setLayers(new Set(prefs.layers_on));
    if (!touchedRef.current.preferred) setPreferredOnlyState(prefs.preferred_only);
    if (!touchedRef.current.weather) setShowWeather(prefs.weather_on);
    if (!touchedRef.current.radar) setShowRadar(prefs.radar_on && radarOn); // the Radar default of the Trip planner settings
  }, [prefsSettled, prefs, radarOn]);
  // The open leg tooltip belongs to the map, not to one drawing of the badges: the badges are drawn again when the route's path arrives or the map moves,
  // and that must not close it. It closes when the weather goes off, on another tap, and with the map.
  const legPopupRef = useRef<Popup | null>(null);
  // Effect: the sheet's height changed; once its move is over (0.22 s in CSS), the map follows it. Not while it is still moving: one refit, on settle.
  const followSheetRef = useRef<((snap: "peek" | "half" | "full") => void) | null>(null);
  useEffect(() => {
    if (!sheetSnap) return;
    const t = window.setTimeout(() => followSheetRef.current?.(sheetSnap), 260);
    return () => window.clearTimeout(t);
  }, [sheetSnap]);
  // The radar strip says light, moderate and heavy on one line; where that does not fit, "moderate" goes (data-short, CSS), never a wrap. Measured
  // with it in, every time, so a wider window brings it back.
  const stripRef = useRef<HTMLSpanElement | null>(null);
  useLayoutEffect(() => {
    const el = stripRef.current;
    if (!el) return;
    const check = () => {
      el.removeAttribute("data-short");
      if (el.scrollWidth > el.clientWidth + 1) el.setAttribute("data-short", "");
    };
    check();
    window.addEventListener("resize", check);
    return () => window.removeEventListener("resize", check);
  }, [radarLabel]);
  useEffect(() => {
    // ...or when the numbers change (a new departure time, other units): the card would show the old ones under new badges.
    legPopupRef.current?.remove(); legPopupRef.current = null;
  }, [showWeather, hasWeather, weatherKey]);
  useEffect(() => () => { legPopupRef.current?.remove(); legPopupRef.current = null; }, []);
  // Effect: one weather badge per leg while the "Weather" chip is on. A tap on one opens that leg's details.
  useEffect(() => {
    const map = mapReady;
    const legs = legWeatherRef.current;
    if (!map || !showWeather || !legs || !hasWeather) return;
    let alive = true;
    let remove: BadgesHandle = () => {};
    let redraw: (() => void) | null = null;
    let later: (() => void) | null = null;
    let timer: number | undefined;
    void loadMapLibre().then((ml) => {
      if (!alive || mapRef.current !== map) return;
      const stops = route.Waypoints.map((w) => [w.Latitude, w.Longitude] as LatLng);
      const draw = (prev?: BadgesHandle) => drawLegBadges(ml, map, routePath, stops, legs, appMapFlavor(), (info, at) => {
        legPopupRef.current?.remove();
        const lines = [legWeatherTags(info.facts).join(" · "), ...info.note].filter(Boolean);
        const root = document.createElement("div");
        root.setAttribute("data-leg-popup", "");
        const h = document.createElement("div");
        h.style.fontWeight = "600";
        h.textContent = info.title;
        root.appendChild(h);
        for (const l of lines) {
          const d = document.createElement("div");
          d.textContent = l;
          root.appendChild(d);
        }
        // Pinned so it stays inside the map: below the badge in the upper half, above it in the lower, and sideways toward the roomier side, with the
        // card narrowed to the room that side has (a phone's map is 350 px wide: a 280 px card from a badge at 95 px ran 20 px past the edge).
        const ll = { lng: at[1], lat: at[0] };
        const side = fitSideways(map, ll, 280);
        const legPopup = new ml.Popup({ closeButton: true, offset: 14, maxWidth: `${side.width}px`, className: "rivolt-popup" });
        const vertical = map.project(ll).y < mapRoomBottom(map) / 2 ? "top" : "bottom";
        // A short map (a small phone) cannot hold every line: the card scrolls inside the room its side has, instead of running under the page.
        const roomY = vertical === "top" ? mapRoomBottom(map) - map.project(ll).y : map.project(ll).y;
        root.style.maxHeight = `${Math.max(120, Math.floor(roomY - 24))}px`;
        root.style.overflowY = "auto";
        if (legPopup.options) legPopup.options.anchor = (side.side ? `${vertical}-${side.side}` : vertical) as never;
        legPopup.setLngLat([at[1], at[0]]).setDOMContent(root).addTo(map);
        legPopupRef.current = legPopup;
        exclusive(legPopup);
        reclearOfChips(map, legPopup, ll, true);
        // The room is measured again when the map stops moving (the pan to a selected stop, the nudge clear of the chips, a drag) and when the phone's
        // sheet changes height: a card fitted once at open ran out of the map or under the sheet, or kept scrolling a line away (#1887).
        const refit = () => {
          if (!legPopup.isOpen()) return;
          const y = map.project(ll).y;
          const below = !String(legPopup.options?.anchor ?? vertical).startsWith("bottom");
          // The card is the text plus its padding, 14 px off its point, and 8 px clear of the edge.
          const card = root.parentElement;
          const chrome = card ? Math.max(0, card.offsetHeight - root.clientHeight) : 24;
          root.style.maxHeight = `${Math.max(72, Math.floor((below ? mapRoomBottom(map) - y : y) - 14 - chrome - 8))}px`;
        };
        const sheet = document.querySelector("[data-plan-sheet]");
        const ro = sheet && typeof ResizeObserver !== "undefined" ? new ResizeObserver(refit) : null;
        if (sheet) ro?.observe(sheet);
        map.on("moveend", refit);
        legPopup.once("close", () => { map.off("moveend", refit); ro?.disconnect(); });
      }, overlayBoxes(map.getContainer()), pillTakenRef.current, prev);
      remove = draw();
      updatePillsRef.current?.(true);
      // The pills move with the zoom and the pan: the badges are placed again clear of the pins, and the pills go round them.
      redraw = () => { const next = draw(remove); if (next !== remove) { remove(); remove = next; } updatePillsRef.current?.(true); };
      // While the map moves (a pan with its glide, a zoom) the badges are placed again at most every 150 ms, and when it stops.
      later = () => { timer ??= window.setTimeout(() => { timer = undefined; redraw?.(); }, 150); };
      map.on("move", later);
      map.on("moveend", redraw);
    });
    return () => {
      alive = false;
      if (redraw) map.off("moveend", redraw);
      if (later) map.off("move", later);
      if (timer !== undefined) window.clearTimeout(timer);
      remove();
    };
  }, [mapReady, showWeather, weatherKey, hasWeather, routePath, route.Waypoints]);
  // Refs so async handlers call the latest callbacks without being effect dependencies (which would re-mount the map every render).
  const onAddStopRef = useRef(onAddStop);
  useEffect(() => { onAddStopRef.current = onAddStop; }, [onAddStop]);
  const onAddOvernightRef = useRef(onAddOvernight);
  useEffect(() => { onAddOvernightRef.current = onAddOvernight; }, [onAddOvernight]);
  // One callout at a time: opening a stop callout, a dropped pin or a charger popup closes the others (a right click near a marker used to open two).
  const livePopupsRef = useRef<Popup[]>([]);
  const exclusive = (p: Popup) => {
    for (const q of livePopupsRef.current) if (q !== p) q.remove();
    livePopupsRef.current = [p];
  };
  // The layer chips sit over the top of the map and paint over a popup that reaches them, so a point in the upper part opens its popup below it.
  // Where the usable map ends at the bottom: its own edge, or the top of the phone's bottom sheet when that covers the lower part of it, less the
  // radar strip that rides the sheet's edge (reserved whether or not it is drawn yet, as the route's fit does: it can land after a popup opens).
  const mapRoomBottom = (map: MLMap) => {
    const top = map.getContainer().getBoundingClientRect?.().top ?? 0;
    const sheet = document.querySelector("[data-plan-sheet]")?.getBoundingClientRect().top;
    const strip = radarOnRef.current ? 28 : 0;
    return sheet != null && sheet > top ? Math.min(map.getContainer().clientHeight, sheet - top - strip) : map.getContainer().clientHeight;
  };
  // A pinned vertical side (top = below the point, bottom = above it) must still lean left or right near the map's edges, as MapLibre's own pick would,
  // or the popup runs past the edge: a point in the left or right half-width of the popup anchors it by that corner.
  const pinSide = (map: MLMap, vertical: "top" | "bottom", ll: { lng: number; lat: number }, widthPx: number): "top" | "bottom" | "top-left" | "top-right" | "bottom-left" | "bottom-right" => {
    const x = map.project(ll).x;
    const w = map.getContainer().clientWidth;
    return x < widthPx / 2 ? `${vertical}-left` : x > w - widthPx / 2 ? `${vertical}-right` : vertical;
  };
  // Where a card of up to `want` px can sit beside a point without leaving the map: centred when both sides have room, else toward the roomier
  // side, at the width that side has (never below 160 px).
  const fitSideways = (map: MLMap, ll: { lng: number; lat: number }, want: number): { side: "left" | "right" | ""; width: number } => {
    const x = map.project(ll).x;
    const w = map.getContainer().clientWidth;
    const margin = 18; // the card's 14 px offset from the point leans diagonally from a corner anchor
    if (Math.min(x, w - x) * 2 - margin * 2 >= want) return { side: "", width: want };
    const roomRight = w - x - margin;
    const roomLeft = x - margin;
    return roomRight >= roomLeft ? { side: "left", width: Math.max(160, Math.min(want, Math.floor(roomRight))) } : { side: "right", width: Math.max(160, Math.min(want, Math.floor(roomLeft))) };
  };
  const keepClearOfChips = (map: MLMap, p: Popup, ll: { lng: number; lat: number }) => {
    const h = mapRoomBottom(map);
    const y = map.project(ll).y;
    if (p.options) p.options.anchor = y < h * 0.45 && y + 300 < h ? pinSide(map, "top", ll, 280) : undefined;
  };
  // The last word on the sides: after it is placed, a popup whose box leaves the map on the left or the right is re-anchored by that corner (its point
  // sits near that edge), once the browser has laid it out. The pinned anchors above judge by a width that can still change.
  const clampWatched = useRef(new Set<Popup>());
  const clampSideways = (map: MLMap, p: Popup) => {
    const fix = () => {
      const pe = p.getElement();
      if (!pe || !p.options || !p.isOpen()) return;
      const m = map.getContainer().getBoundingClientRect();
      const b = pe.getBoundingClientRect();
      if (b.left >= m.left + 2 && b.right <= m.right - 2) return;
      // It leaves the map: pick the side again from where its point is now, as when it opened (centred, else toward the roomier side, at the width that
      // side has).
      const a = String(p.options.anchor ?? [...pe.classList].find((c) => c.startsWith("maplibregl-popup-anchor-"))?.replace("maplibregl-popup-anchor-", "") ?? "top");
      const vertical = a.startsWith("bottom") ? "bottom" : "top";
      const want = Number.parseFloat(String(p.options.maxWidth)) || b.width;
      const ll = p.getLngLat();
      const side = fitSideways(map, ll, Math.max(want, 160));
      p.options.anchor = (side.side ? `${vertical}-${side.side}` : vertical) as never;
      p.setMaxWidth(`${side.width}px`);
      p.setLngLat(ll);
    };
    requestAnimationFrame(fix);
    window.setTimeout(fix, 250);
    // The map can move after the popup is placed (the pan to a selected stop, the nudge clear of the chips, the visitor dragging it): look again when it stops.
    if (!clampWatched.current.has(p)) {
      clampWatched.current.add(p);
      map.on("moveend", fix);
      p.once("close", () => {
        map.off("moveend", fix);
        clampWatched.current.delete(p);
      });
    }
  };
  // A popup's text can arrive after it opens (the what-if line, a charger's preview) and grow it into the chips above or under the phone's bottom sheet.
  // MapLibre re-picks an unset anchor on every move, so pin one: below the point when it fits there, else above it when that clears the chips, else below
  // with the map nudged up by the shortfall.
  const reclearOfChips = (map: MLMap, p: Popup, ll: { lng: number; lat: number }, keepInside = false) => {
    const chips = map.getContainer().parentElement?.querySelector<HTMLElement>("[data-map-chips]");
    const pe = p.getElement();
    if (!p.options || !chips || !pe) return;
    const c = chips.getBoundingClientRect();
    const b = pe.getBoundingClientRect();
    const cr = map.getContainer().getBoundingClientRect();
    const limit = cr.top + mapRoomBottom(map);
    // keepInside (the leg tooltip only) also re-picks the side whenever the card leaves the map; other popups keep their own placement rules.
    // The card's width as laid out right now can be short of its final width (its text is not all in yet, a web font is still loading): pin the side for the
    // widest it is allowed to get, or a point near an edge is judged to have room and the card runs past it.
    const wide = (keepInside ? Math.max(b.width, Number.parseFloat(String(p.options.maxWidth)) || 0) : b.width) + 16;
    const underChips = !(b.right <= c.left || b.left >= c.right) && b.top < c.bottom && b.bottom > c.top;
    if (!underChips && b.bottom <= limit) { if (keepInside) clampSideways(map, p); return; }
    const pointY = cr.top + map.project(ll).y;
    const room = limit - pointY - 24;
    const roomAbove = pointY - c.bottom - 24;
    if (b.height <= room) {
      p.options.anchor = pinSide(map, "top", ll, wide);
    } else if (b.height <= roomAbove) {
      p.options.anchor = pinSide(map, "bottom", ll, wide);
    } else {
      p.options.anchor = pinSide(map, "top", ll, wide);
      p.setLngLat(ll);
      // Below its point the popup spans pointY + 8 (the popup's offset) to that plus its height. Move the map up by what lifts its bottom 8 px clear of the
      // limit, but never so far that its top leaves the map (the old height-based sum overshot when the popup had been above its point). A positive y moves
      // the content, and the popup with it, up.
      const top = pointY + 8;
      const needUp = Math.ceil(top + b.height - (limit - 8));
      const maxUp = Math.max(0, Math.floor(top - (cr.top + 4)));
      map.panBy([0, Math.max(0, Math.min(needUp, maxUp))], { duration: 200 }, { rivoltPan: true });
      if (keepInside) clampSideways(map, p);
      return;
    }
    p.setLngLat(ll);
    if (keepInside) clampSideways(map, p);
  };
  const onSelectStopRef = useRef(onSelectStop);
  useEffect(() => { onSelectStopRef.current = onSelectStop; }, [onSelectStop]);
  const { distanceUnit } = usePreferences();
  const distanceUnitRef = useRef(distanceUnit);
  distanceUnitRef.current = distanceUnit;
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
      styleReadyRef.current = false;
      el.setAttribute("data-flavor", flavor);
      // data-map-drawn: the route's features rendered in view (0 until the line is there); a spec waits for it before it looks (drawn.ts).
      const drawn = watchDrawn(map, el, "rivolt-track-main");
      cleanups.push(drawn.stop);
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
      const padding = () => {
        // The legend card sits at the bottom left: leave room under the route for its height, but never so much that the padding leaves the route
        // under 60 px of the map's height (MapLibre refuses a fit whose padding does not fit, and the route is then not framed at all).
        // A card that is not drawn (hidden under 200 px, where the sheet's peek says the same) needs no room.
        const tripCardEl = el.querySelector<HTMLElement>(".rivolt-trip-card");
        const card = tripCardEl?.getClientRects().length ? tripCardEl : null;
        // On a phone the card rides above the sheet, and above the radar strip on its edge (reserved whether or not the radar is on, as at the top).
        const lift = card ? card.offsetHeight + (parseFloat(getComputedStyle(card).marginBottom) || 0) + 28 + (sheetEdge() ? sheetCover() + (radarOnRef.current ? 28 : 0) : 0) : 0;
        // In the fill layout the charger chips float over the map's top: clear their real height (they wrap to several rows on a phone).
        const chips = fill ? el.parentElement?.querySelector<HTMLElement>("[data-map-chips]") : null;
        const chipsBottom = chips ? chips.getBoundingClientRect().bottom - el.getBoundingClientRect().top + 12 : 0;
        // ...and the radar strip under them (one line, and the label of a pin that sits just under it), when this visitor has radar: reserved whether or not it is on, so turning it on covers no pin.
        // On a phone the strip rides the sheet's edge instead, inside the bottom's room.
        const top = fill ? Math.max(narrow ? 96 : 64, chipsBottom + (radarOnRef.current && !sheetEdge() ? 52 : 0)) : 24;
        const base = fill ? (narrow ? 40 : 24) : 24;
        // The phone's sheet covers the lower part of the map at half: the route clears it and the strip on its edge.
        const cover = sheetEdge() ? sheetCover() + (radarOnRef.current ? 28 : 0) + 12 : 0;
        const bottom = Math.max(base, Math.min(Math.max(lift, cover), el.clientHeight - top - 60));
        return { top, bottom, left: 24, right: 24 };
      };
      // How much of the map the phone's sheet covers right now (0 without a sheet): the credits are lifted by as much, so they stay in view.
      const sheetEdge = () => !!fill && !!document.querySelector("[data-plan-sheet]");
      const sheetCover = () => {
        const sheet = document.querySelector("[data-plan-sheet]");
        if (!sheet) return 0;
        const r = el.getBoundingClientRect();
        return Math.round(Math.min(Math.max(0, r.bottom - sheet.getBoundingClientRect().top), Math.max(0, el.clientHeight - 32)));
      };
      const liftCredits = () => el.style.setProperty("--plan-map-cover", `${sheetCover()}px`);
      let fitPath: LatLng[] = wpLatLngs;
      let fitted = false;
      const fit = (maxZoom: number) => {
        liftCredits();
        if (!(el.clientWidth > 0 && el.clientHeight > 0)) { fitted = false; return; }
        map.fitBounds(boundsOfPath(fitPath), { padding: padding(), maxZoom, animate: false });
        fitted = true;
        updatePills();
      };
      let fitZoom = 12;

      // The route in one colour (the accent teal of the drive ramp), with the drive map's glow and casing, straight legs first and the real geometry
      // when it arrives; re-added after every style change. A plan has no per-sample metric to colour by, so the markers carry the meaning, and a
      // one-line card says what the plan is: its day, distance, charging stops and time.
      const km = localUnits().distanceUnit;
      let track: LatLng[] = wpLatLngs;
      let card: ReturnType<typeof tripCard> | undefined;
      let cardOn = false;
      const cardText = () =>
        tripCardText({ day: dayLabel, meters: route.TotalDriveDistanceMeters, stops: chargingStopCount(route), seconds: route.TotalTripDurationSec, unit: km });
      const draw = () => {
        const c = RAMPS[flavor].speed[1];
        setDriveRoute(map, { track, stops: [0, c, 1, c] }, flavor, firstLabelLayer(map));
        const text = cardText();
        if (text) {
          if (!card) card = tripCard({ text, flavor });
          else card.update({ text, flavor });
          if (!cardOn) {
            map.addControl(card, "bottom-left");
            cardOn = true;
          }
        }
        updatePills();
        drawn.markDrawn();
      };
      // ---- place-name pills: the origin and destination by name (a dot in the colour of that end of the route) and each charging stop's time and
      // battery, as map symbols so the basemap's labels yield to them; on the side of each pin with more room, and a pill that would lie over
      // another is dropped (the origin and destination first).
      const shortName = (n: string) => {
        const part = n.split(",")[0].trim();
        const max = narrow ? 18 : 30;
        return part.length > max ? part.slice(0, max - 1) + "…" : part;
      };
      // awayPoint is a point of the route about 40 px along from the origin (or back from the destination), on the screen: where the line goes from the pin.
      const awayPoint = (fromStart: boolean) => {
        const pin = map.project(fromStart ? [track[0][1], track[0][0]] : [track[track.length - 1][1], track[track.length - 1][0]]);
        for (let k = 1; k < track.length; k++) {
          const v = fromStart ? track[k] : track[track.length - 1 - k];
          const p = map.project([v[1], v[0]]);
          if (Math.hypot(p.x - pin.x, p.y - pin.y) >= 40) return p;
        }
        return null;
      };
      const pillKeyRef = { key: "" };
      const updatePills = (force = false) => {
        if (!styleReadyRef.current) return;
        const last = ws.length - 1;
        const view = { w: map.getContainer().clientWidth || 1, h: map.getContainer().clientHeight || 1 };
        // What is already taken: every pin (a disc and its ring), and the end pills, which win over a stop's.
        const pinBoxes: Box[] = ws.map((w) => {
          const p = map.project([w.Longitude, w.Latitude]);
          return { x0: p.x - 14, y0: p.y - 14, x1: p.x + 14, y1: p.y + 14 };
        });
        pillTakenRef.current = pinBoxes;
        // The leg badges are placed first and win: a pill goes round them (to the pin's other side or above or below), or is dropped.
        const here = el.getBoundingClientRect();
        const badgeBoxes: Box[] = Array.from(el.querySelectorAll<HTMLElement>("[data-leg-badge]")).map((b) => {
          const r = b.getBoundingClientRect();
          return { x0: r.left - here.left, y0: r.top - here.top, x1: r.right - here.left, y1: r.bottom - here.top };
        });
        // What floats over the map (the chips, the radar strip, the summary card) is taken too: a pill goes round it, never under it.
        const taken: Box[] = [...pinBoxes, ...badgeBoxes, ...overlayBoxes(el)];
        const specs: PillSpec[] = [];
        const ends = [0, last].filter((i, k, a) => a.indexOf(i) === k && !(i === last && last > 0 && sameSpot(ws[0], ws[last]))); // a round trip: one pill
        for (const i of ends) {
          const w = ws[i];
          const ll: [number, number] = [w.Longitude, w.Latitude];
          // The ends sit where the route leaves or arrives: the pill goes on the side away from the line, a little off its height.
          const away = pillAwayFromLine(map.project(ll), awayPoint(i === 0), view.w);
          const name = (i === 0 ? originLabel : destLabel) || w.Name || (i === 0 ? "Origin" : "Destination");
          const text = shortName(name);
          const size = measurePill(text, true);
          // Away from the line, unless that runs past the map's edge or over a pin or badge: then the first side that fits (else where it was).
          const side = (size && placeStopPill(map.project(ll), size, away, taken.filter((b) => b !== pinBoxes[i]), view)) || away;
          if (size) taken.push(pillBox(map.project(ll), side.anchor, side.offset, size));
          specs.push({ id: `wp${i}`, text, lngLat: ll, ...side, dot: i === 0 ? "#10b981" : "#f43f5e", priority: i === 0 ? 0 : 1 });
        }
        // A stop's pill goes on its usual side, or the other side, above or below, wherever it clears what is taken; none fits: dropped.
        for (let i = 1; i < last; i++) {
          const w = ws[i];
          const ll: [number, number] = [w.Longitude, w.Latitude];
          const min = Math.round(w.ChargeDurationSec / 60);
          const text = w.ChargeDurationSec > 0 ? `${min} min · ${Math.round(w.ArrivalSoC)}→${Math.round(w.DepartureSoC)}%` : shortName(w.Name || "Stop");
          const size = measurePill(text);
          const usual = pillSide(map, ll);
          const pin = map.project(ll);
          const at = size ? placeStopPill(pin, size, usual, taken.filter((b) => b !== pinBoxes[i]), view) : usual; // its own pin is not in its way
          if (!at) continue;
          if (size) taken.push(pillBox(pin, at.anchor, at.offset, size));
          specs.push({ id: `wp${i}`, text, lngLat: ll, ...at, priority: 2 });
        }
        const key = JSON.stringify(specs.map((p) => [p.id, p.text, p.anchor, p.offset, p.dot])) + flavor;
        if (!force && key === pillKeyRef.key) return;
        pillKeyRef.key = key;
        setPills(map, specs, flavor, true);
      };
      // The pills were placed for the view the route was fitted in: while the map moves (and when it stops) they are placed again, so one near the map's edge flips
      // to the pin's other side instead of running past it.
      map.on("move", () => updatePills());
      map.on("moveend", () => updatePills());
      updatePillsRef.current = updatePills;
      map.on("style.load", () => {
        styleReadyRef.current = true;
        cardOn = false; // setStyle keeps controls only on the same map; the card goes back
        draw();
        // The charger overlay goes back too.
        addChargerLayer(map);
      });

      // Waypoint markers: click selects the stops-table row; hovering shows the tooltip.
      wpMarkersRef.current.clear();
      const tip = new ml.Popup({ closeButton: false, closeOnClick: false, offset: 12, className: "rivolt-tip", maxWidth: "260px" });
      const callout = new ml.Popup({ closeButton: true, closeOnClick: true, offset: 14, className: "rivolt-tip rivolt-live", maxWidth: "260px" });
      for (let i = 0; i < ws.length; i++) {
        const w = ws[i];
        const origIdx = wsWithIdx[i].i;
        const roundTrip = ws.length > 1 && sameSpot(ws[0], ws[ws.length - 1]);
        if (roundTrip && i === ws.length - 1) continue; // one marker for start and end
        const endWp = roundTrip && i === 0 ? ws[ws.length - 1] : undefined;
        const isEnd = i === 0 || i === ws.length - 1;
        const color = i === 0 ? "#10b981" : i === ws.length - 1 ? "#f43f5e" : "#f59e0b";
        const size = isEnd ? 12 : 10;
        // The marker's own element keeps the library's classes and a fixed box (a steady anchor and a touch target); the dot inside swaps on selection.
        const element = document.createElement("div");
        element.style.cssText = "display:flex;align-items:center;justify-content:center;width:28px;height:28px;";
        element.appendChild(dotElement(color, size, false, !isEnd && isChargingWaypoint(w)));
        element.style.cursor = "pointer";
        const marker = new ml.Marker({ element }).setLngLat([w.Longitude, w.Latitude]).addTo(map);
        // A tap or click selects the stops-table row and opens the stop's callout (a touch screen has no hover); hovering shows the same words.
        element.addEventListener("click", (ev) => {
          ev.stopPropagation();
          onSelectStopRef.current?.(origIdx);
          tip.remove(); // a touch tap also fires mouseenter: one popup, the callout with its close button
          callout.remove();
          // WebKit's tap also reaches the map as a click, which would close a popup opened in the same turn: open it just after.
          window.setTimeout(() => {
            if (!alive || mapRef.current !== map) return;
            keepClearOfChips(map, callout, { lng: w.Longitude, lat: w.Latitude });
            callout.setLngLat([w.Longitude, w.Latitude]).setHTML(waypointTooltip(w, i, ws.length, endWp)).addTo(map);
            exclusive(callout);
          }, 40);
        });
        element.addEventListener("mouseenter", () => { if (!callout.isOpen()) tip.setLngLat([w.Longitude, w.Latitude]).setHTML(waypointTooltip(w, i, ws.length, endWp)).addTo(map); });
        element.addEventListener("mouseleave", () => tip.remove());
        const bolt = !isEnd && isChargingWaypoint(w);
        wpMarkersRef.current.set(origIdx, { marker, color, size, wrap: element, bolt });
        // A stop picked before its marker existed (the map was still starting) or before the markers were drawn again: the marker comes up already selected.
        if (selectedIdxRef.current === origIdx) {
          element.replaceChildren(dotElement(color, size, true, bolt));
          element.style.zIndex = "1000";
        }
      }
      // A long press (touch) or a right click (mouse) drops a pin with an "Add as a stop" callout. Only where stops can be added (the signed-in planner).
      const drop = new ml.Popup({ closeButton: true, closeOnClick: true, offset: 8, className: "rivolt-tip rivolt-live", maxWidth: "240px" });
      // What adding the stop here would do, from our own router: the change, an estimate, nothing committed. The latest drop wins; a failure shows nothing
      // (busy says so).
      let previewCtl: AbortController | null = null;
      // The what-if text can make the callout taller than the map leaves it (the phone's map is short, the sheet covers the rest): cap just the text's
      // height to what fits and let it scroll, so the title and Add as a stop stay in view.
      const capPreview = (into: HTMLElement) => {
        const pe = drop.getElement();
        if (!pe) return;
        const chrome = pe.getBoundingClientRect().height - into.getBoundingClientRect().height; // title, button, caption, tip
        const room = mapRoomBottom(map) - chrome - 24;
        if (into.scrollHeight <= room) return;
        into.style.maxHeight = `${Math.max(72, Math.floor(room))}px`;
        into.style.overflowY = "auto";
        into.tabIndex = 0;
      };
      const previewAdd = (ll: { lng: number; lat: number }, into: HTMLElement | null) => {
        previewCtl?.abort();
        previewCtl = null;
        if (!into || inDemo()) return;
        const req = addRequest(route, { lat: ll.lat, lon: ll.lng });
        if (!req) return;
        const ctl = new AbortController();
        previewCtl = ctl;
        backend.whatIf(req, ctl.signal).then(
          (r) => {
            if (ctl.signal.aborted || !into.isConnected) return;
            const lines = whatIfLines(r, distanceUnitRef.current);
            into.replaceChildren(...lines.map((t, k) => {
              const d = document.createElement("div");
              d.textContent = t;
              if (k === 0) d.style.fontWeight = "600";
              else if (t === WHAT_IF_NOTICE || t === WHAT_IF_ROUGH) { d.style.color = "#a3a3a3"; d.style.fontSize = `${MAP_CAPTION_PX}px`; }
              return d;
            }));
            capPreview(into);
            reclearOfChips(map, drop, ll);
          },
          (e: unknown) => {
            const line = ctl.signal.aborted || !into.isConnected ? null : whatIfFailureLine((e as { status?: number })?.status);
            if (line) { into.textContent = line; reclearOfChips(map, drop, ll); }
          },
        );
      };
      const openDrop = (ll: { lng: number; lat: number }) => {
        if (!onAddStopRef.current) return;
        drop.remove();
        keepClearOfChips(map, drop, ll);
        drop.setLngLat(ll).setHTML(droppedPinPopupHTML()).addTo(map);
        exclusive(drop);
        previewAdd(ll, drop.getElement()?.querySelector<HTMLElement>(".drop-preview") ?? null);
        drop.getElement()?.querySelector<HTMLButtonElement>(".drop-add-btn")?.addEventListener("click", () => {
          onAddStopRef.current?.({ lat: ll.lat, lon: ll.lng, label: DROPPED_PIN_LABEL });
          drop.remove();
        });
      };
      let pressTimer: number | undefined;
      const cancelPress = () => { if (pressTimer !== undefined) { window.clearTimeout(pressTimer); pressTimer = undefined; } };
      map.on("contextmenu", (e) => openDrop(e.lngLat));
      map.on("touchstart", (e) => {
        cancelPress();
        if (e.points && e.points.length > 1) return;
        const ll = e.lngLat;
        pressTimer = window.setTimeout(() => openDrop(ll), 550);
      });
      for (const ev of ["touchmove", "touchend", "touchcancel", "dragstart", "zoomstart"] as const) map.on(ev, cancelPress);
      cleanups.push(() => { cancelPress(); previewCtl?.abort(); drop.remove(); });
      cleanups.push(() => { tip.remove(); callout.remove(); wpMarkersRef.current.forEach((m) => m.marker.remove()); wpMarkersRef.current.clear(); });

      // The basemap follows the app theme live (Settings, or the OS under System).
      cleanups.push(onAppMapFlavor((f) => {
        if (f === flavor) return;
        flavor = f;
        styleReadyRef.current = false;
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
      // The chips grow when their "no chargers here" note appears (after the fit): reframe so the route still clears them, until the user moves
      // the map themselves.
      let touched = false;
      map.on("dragstart", () => { touched = true; });
      // Our own nudge that clears a popup counts as the map having been moved: a later chips resize must not reframe it back.
      map.on("movestart", (e) => { if ((e as { rivoltPan?: boolean }).rivoltPan) touched = true; });
      map.on("zoomstart", (e) => { if ((e as { originalEvent?: unknown }).originalEvent) touched = true; });
      const chipsEl = fill ? el.parentElement?.querySelector<HTMLElement>("[data-map-chips]") : null;
      let chipsH = chipsEl?.offsetHeight ?? 0;
      const roChips = new ResizeObserver(() => {
        if (!alive || mapRef.current !== map || touched || !chipsEl || chipsEl.offsetHeight === chipsH) return;
        chipsH = chipsEl.offsetHeight;
        fit(fitZoom);
      });
      if (chipsEl) roChips.observe(chipsEl);
      // The sheet settled at another height: lift the credits to its edge and reframe the route above it, unless the person has moved the map (a new
      // plan starts this effect again, unmoved). Fully open, the map is out of sight: it keeps its frame.
      followSheetRef.current = (snap) => {
        if (!alive || mapRef.current !== map) return;
        liftCredits();
        if (!touched && snap !== "full") fit(fitZoom);
      };
      cleanups.push(() => { followSheetRef.current = null; });
      reframeRef.current = () => { if (alive && mapRef.current === map && !touched) fit(fitZoom); };
      cleanups.push(() => { reframeRef.current = null; });
      cleanups.push(() => { ro.disconnect(); roChips.disconnect(); if (roRaf) cancelAnimationFrame(roRaf); });
      // The map's first full load can land after the person (or our own popup nudge) has already moved it: do not reframe it back.
      map.once("load", () => { if (!touched) fit(fitZoom); });
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
        track = path;
        if (styleReadyRef.current) draw(); // else the pending style.load draws it
        fitPath = path;
        fitZoom = 13;
        fit(fitZoom);
      }
      setRoutePath(path);
    })().catch((err) => console.warn("map failed to start:", err instanceof Error ? err.message : err));

    return () => { for (const c of cleanups.reverse()) c(); };
  }, [supported, route, fill, dayLabel, originLabel, destLabel]);

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
          ? campPopupHTML(poi, camp, !!onAddOvernightRef.current, offRouteLine(poi.offKm, localUnits().distanceUnit))
          : chargerPopupHTML(poi, !!onAddStopRef.current, !!onPreviewStopRef.current, isHotel ? bookingDeepLink(poi, departureAtRef.current) : "", isHotel && !!onAddOvernightRef.current, offRouteLine(poi.offKm, localUnits().distanceUnit));
        const popup = new ml.Popup({ closeButton: true, offset: 8, maxWidth: "280px", className: "rivolt-popup" });
        keepClearOfChips(map, popup, { lng: poi.lon, lat: poi.lat });
        popup.setLngLat([poi.lon, poi.lat]).setHTML(html).addTo(map);
        exclusive(popup);
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
          if (mapReady) reclearOfChips(mapReady, popup, { lng: poi.lon, lat: poi.lat });
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
      m.wrap.replaceChildren(dotElement(m.color, m.size, selected, m.bolt));
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
    if (routePath.length < 2 || !map || !started) return;
    const ac = new AbortController();
    const kinds = layersKey ? (layersKey.split(",") as LayerKind[]) : [];
    // Every chip off: draw nothing and ask for nothing.
    if (kinds.length === 0) {
      pois.current = [];
      chargersRef.current = { type: "FeatureCollection", features: [] };
      setChargerCount(0);
      setChargerTotal(0);
      setChargersLoading(false);
      const src = map.getSource(CHARGERS_SOURCE) as GeoJSONSource | undefined;
      if (src && styleReadyRef.current) src.setData(chargersRef.current as never);
      return;
    }
    setChargersLoading(true);
    setLayerError(false);
    void (async () => {
      // "Preferred only" narrows the fast layer to the preferred networks; the other layers on are unchanged.
      const others = onlyPreferred ? kinds.filter((k) => k !== "dcfc") : kinds;
      let pref: POI[] | null, rest: { pois: POI[]; total: number } | null;
      try {
        [pref, rest] = await Promise.all([
          onlyPreferred ? findChargersOfNetworks(routePath, preferredKey.split(","), ac.signal, filters) : Promise.resolve([] as POI[]),
          findLayersAlongPath(routePath, others, ac.signal, filters),
        ]);
      } catch {
        // A chip toggled while this request was in flight cancels it (the next effect run asks again). Any other failure keeps the pins as they were
        // and says so.
        if (!ac.signal.aborted) { setChargersLoading(false); setLayerError(true); }
        return;
      }
      const found = pref === null || rest === null ? null : [...pref, ...rest.pois];
      const total = pref === null || rest === null ? 0 : pref.length + rest.total;
      if (ac.signal.aborted || mapRef.current !== map) return;
      setNoChargerSource(found === null);
      const chargers = found ?? [];
      setChargerCount(chargers.length);
      setChargerTotal(total);
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
      // Until the style is ready, its style.load puts the layer back with chargersRef as it stands.
      if (styleReadyRef.current) apply();
    })();
    return () => {
      ac.abort();
      setChargersLoading(false);
    };
  }, [routePath, layersKey, mapReady, onlyPreferred, preferredKey, started, filtersKey, retry]); // eslint-disable-line react-hooks/exhaustive-deps

  // Is there any campground with a hookup along the route? Decides whether the chip shows at all.
  useEffect(() => {
    if (routePath.length < 2) return;
    let live = true;
    void findLayersAlongPath(routePath, ["camp"])
      .then((found) => { if (live) setCampCount((found?.pois ?? []).filter((p) => campgroundOfPoi(p)).length); })
      .catch(() => { if (live) setCampCount(0); });
    return () => { live = false; };
  }, [routePath]);

  const filterLabels: Record<LayerKind, string> = {
    dcfc: chargerLabel("fast"),
    l2: chargerLabel("medium"),
    hotels: chargerLabel("hotel"),
    camp: chargerLabel("camping"),
  };
  const dotColor: Record<LayerKind, string> = { dcfc: "#06b6d4", l2: "#a3e635", hotels: "#a78bfa", camp: "#22c55e" };

  if (!supported) {
    return (
      <div className={fill ? "h-full" : undefined}>
        <MapUnavailable height={fill ? 240 : height} note="The itinerary beside it is unaffected." />
      </div>
    );
  }

  // The radar legend: on the map it rides next to the Radar chip (not under the Chargers box, where it crowded the chip row and hid a map label); in the
  // flow layout it sits above the map.
  const radarStrip = showRadar && radarLabel ? (
        // One line, as on the iPhone: a short gradient named light, moderate, heavy, then the credit and the picture's time ("NOAA/NWS · 6:42 PM").
        // NOAA's colours as they are drawn. A feed that is down or stale says "Radar unavailable" and draws nothing, so nothing reads as "no rain".
        <span
          ref={stripRef}
          role="status"
          aria-label={radarSpokenLabel}
          data-radar-label
          className={`pointer-events-auto flex w-fit ${fill ? "max-w-[min(20rem,calc(100vw-6rem))]" : "max-w-full"} items-center gap-1.5 overflow-hidden whitespace-nowrap rounded-full bg-neutral-950/85 px-2 py-0.5 text-[10px] text-neutral-200 ${fill ? "" : "mb-1"}`}
        >
          {radarLabel.startsWith("NOAA") && (
            <span aria-hidden="true" className="flex items-center gap-1 text-neutral-400">
              light
              <i data-strip-bar="low" className="inline-block h-1.5 w-5 rounded-full" />
              <span data-strip-mid className="flex items-center gap-1">moderate<i data-strip-bar="high" className="inline-block h-1.5 w-5 rounded-full" /></span>
              heavy
            </span>
          )}
          <span aria-hidden="true">{radarLabel}</span>
        </span>
  ) : null;
  return (
    <div data-trip-map className={fill ? "relative isolate h-full" : "isolate"}>
      {/* The chips and, under them, the radar strip: one column at the map's top left, so the strip never lands on the route. */}
      <div className={fill ? "pointer-events-none absolute left-14 right-2 top-3 z-[500] flex flex-col items-start gap-1 max-[200px]:left-12 max-[200px]:right-1" : "contents"}>
      {!noChargerSource && (
        <div className={fill ? "pointer-events-auto flex flex-wrap items-center gap-1 rounded-lg bg-neutral-950/80 px-2 py-1.5 backdrop-blur-sm max-[200px]:px-1" : "flex flex-wrap items-center gap-1 mb-1"} data-plan-chips={fill ? "" : undefined} data-map-chips>
          {!noChargerSource && <>
          <span className="whitespace-nowrap text-xs text-neutral-500 mr-1 max-[200px]:sr-only">Chargers:</span>
          {(["dcfc", "l2", "hotels", "camp"] as const).filter((f) => f !== "camp" || campCount > 0 || layers.has("camp")).map((f) => (
            <button
              key={f}
              aria-pressed={layers.has(f)}
              onClick={() => toggleLayer(f)}
              className={`whitespace-nowrap px-2 py-0.5 text-xs rounded transition-colors max-[200px]:px-1 ${
                layers.has(f) ? "bg-cyan-800 text-cyan-100" : "bg-neutral-800 text-neutral-400 hover:text-neutral-200"
              }`}
            >
              {filterLabels[f]}
            </button>
          ))}
          <ChargerTypeInfo
            kinds={["fast", "medium", "hotel", ...(campCount > 0 || layers.has("camp") ? (["camping"] as ChargerKind[]) : [])]}
          />
          {preferredSlugs.length > 0 && fastOn && (
            <button
              aria-pressed={preferredOnly}
              onClick={() => setPreferredOnly(!preferredOnly)}
              title="Only the networks you prefer in Settings. The planner favours them; it doesn't promise one."
              className={`whitespace-nowrap px-2 py-0.5 text-xs rounded transition-colors ${preferredOnly ? "bg-cyan-800 text-cyan-100" : "bg-neutral-800 text-neutral-400 hover:text-neutral-200"}`}
            >
              Preferred only
            </button>
          )}
          {layerError && !chargersLoading ? (
            <span className="basis-full text-xs text-amber-300" data-layer-error role="status">
              Couldn't load stations along the route.{" "}
              <button type="button" data-layer-retry onClick={() => setRetry((n) => n + 1)} className="underline underline-offset-2 hover:text-amber-200">Try again</button>
            </span>
          ) : layers.size > 0 && !chargersLoading && chargerCount === 0 ? (
            <span className="basis-full text-xs text-neutral-400 max-[200px]:hidden">No chargers in this area at this zoom. Zoom out or move the map.</span>
          ) : (
            <span className="whitespace-nowrap text-xs text-neutral-500 ml-1 max-[200px]:hidden" data-layer-count>
              {layers.size === 0 ? "" : chargersLoading ? "…" : chargerCount != null ? (chargerTotal != null && chargerTotal > chargerCount ? `Showing ${chargerCount} of ${chargerTotal}` : `${chargerCount} found`) : ""}
            </span>
          )}
          {layers.size > 1 && (
            <span className="whitespace-nowrap text-xs text-neutral-600 ml-1 max-[200px]:hidden">
              {(["dcfc", "l2", "hotels", "camp"] as const).filter((k) => layers.has(k)).map((k) => (
                <span key={k}><span style={{ color: dotColor[k] }}>●</span> {filterLabels[k]} &nbsp;</span>
              ))}
            </span>
          )}
          {layers.size === 1 && (
            <span className="text-xs text-neutral-600 ml-1">
              {(["dcfc", "l2", "hotels", "camp"] as const).filter((k) => layers.has(k)).map((k) => (<span key={k}><span style={{ color: dotColor[k] }}>●</span> {filterLabels[k]}</span>))}
            </span>
          )}
          </>}
        </div>
      )}
      </div>
      {!fill && radarStrip}
      {(hasWeather || radarOn) && (
        // Map layers other than chargers sit under the zoom buttons, as the map apps do: the charger row stays one line.
        <div className={fill ? "absolute left-[11px] top-[78px] z-[500] flex items-center gap-2" : "mb-1 flex items-center gap-2"}>
          <button
            type="button"
            aria-label="Weather"
            aria-pressed={showWeather}
            onClick={toggleWeather}
            data-weather-chip
            title="Weather on each leg of the route"
            className={`flex h-8 w-8 items-center justify-center rounded border ${showWeather ? "border-cyan-700 bg-cyan-800 text-cyan-100" : "border-neutral-700 bg-neutral-900/90 text-neutral-400 hover:text-neutral-200"}`}
          >
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>
          </button>
          <span role="status" className="rounded bg-neutral-950/85 px-1.5 py-0.5 text-xs text-neutral-200 empty:hidden">{weatherNote}</span>
        </div>
      )}
      {radarOn && (
        <div className={fill ? "absolute left-[11px] top-[118px] z-[500] flex items-center gap-2" : "mb-1 flex items-center gap-2"}>
          <button
            type="button"
            aria-label="Radar"
            aria-pressed={showRadar}
            onClick={toggleRadar}
            data-radar-chip
            title="Live weather radar (NOAA/NWS)"
            className={`flex h-8 w-8 items-center justify-center rounded border ${showRadar ? "border-cyan-700 bg-cyan-800 text-cyan-100" : "border-neutral-700 bg-neutral-900/90 text-neutral-400 hover:text-neutral-200"}`}
          >
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9" /><circle cx="12" cy="12" r="5" /><path d="M12 12l6-6" /></svg>
          </button>
          {fill && radarStrip}
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
