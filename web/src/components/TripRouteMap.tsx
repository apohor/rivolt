import { useEffect, useRef, useState } from "react";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import { leafletLayer, paintRules, labelRules } from "protomaps-leaflet";
import { namedFlavor } from "@protomaps/basemaps";
import { valhallaBase, tilesPMTilesURL, ensureConfig } from "../lib/config";
import { findChargersAlongPath } from "../lib/poi";
import type { POI } from "../lib/poi";
import type { PlannedWaypoint, TripRoute } from "../lib/api";

// TripRouteMap renders a planned trip on a Leaflet map: a road-
// snapped polyline through every waypoint, plus markers for the
// origin (green), each charging stop (amber), and the destination
// (rose). Falls back to a straight chord between waypoints when
// Valhalla isn't wired or returns nothing.
//
// Valhalla's /route accepts up to 50 locations in one POST so a
// real-world trip (origin + a handful of stops + destination)
// resolves in a single round trip. Shape is returned as polyline6.
type AddStop = (stop: { lat: number; lon: number; label: string }) => void;

export function TripRouteMap({
  route,
  height = 320,
  onAddStop,
  selectedIdx,
  onSelectStop,
}: {
  route: TripRoute;
  height?: number;
  onAddStop?: AddStop;
  // Bidirectional selection between the stops table and the map.
  // selectedIdx is the index into route.Waypoints of the currently
  // highlighted waypoint, or null when nothing is selected. Marker
  // click fires onSelectStop with that waypoint's index so the
  // parent's table row can highlight in sync.
  selectedIdx?: number | null;
  onSelectStop?: (idx: number | null) => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<L.Map | null>(null);
  const chargerLayerRef = useRef<L.LayerGroup | null>(null);
  // Markers keyed by their index in route.Waypoints, so external
  // selection (row click in the table) can pulse the right one.
  const wpMarkersRef = useRef<Map<number, L.Marker>>(new Map());
  const [routePath, setRoutePath] = useState<[number, number][]>([]);
  const [chargerFilter, setChargerFilter] = useState<"dcfc" | "l2" | "all">("dcfc");
  const [chargerCount, setChargerCount] = useState<number | null>(null);
  const [chargersLoading, setChargersLoading] = useState(false);
  // Ref so the async effect can call the latest callback without
  // being in the dep array (which would re-mount the map on every render).
  const onAddStopRef = useRef<AddStop | undefined>(onAddStop);
  useEffect(() => { onAddStopRef.current = onAddStop; }, [onAddStop]);
  const onSelectStopRef = useRef<typeof onSelectStop>(onSelectStop);
  useEffect(() => { onSelectStopRef.current = onSelectStop; }, [onSelectStop]);

  // Effect 1: build map + Valhalla path. Re-runs only on route change.
  useEffect(() => {
    if (!ref.current) return;
    // Filter waypoints with invalid lat/lon but keep their original
    // index in route.Waypoints so marker click can echo the right
    // index back to the parent's selection state.
    const wsWithIdx = route.Waypoints
      .map((w, i) => ({ w, i }))
      .filter(({ w }) =>
        Number.isFinite(w.Latitude) &&
        Number.isFinite(w.Longitude) &&
        !(w.Latitude === 0 && w.Longitude === 0),
      );
    const ws = wsWithIdx.map((x) => x.w);
    if (ws.length < 2) return;

    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    });
    mapRef.current = map;
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    // Leaflet measures the container exactly once at construction. When
    // the wrapping Card mounts inside a flex/grid that hasn't finished
    // layout yet — common after Suspense swaps the lazy chunk in, or
    // when the tab was backgrounded during the lazy load — the
    // measurement comes back 0×0 and the map renders blank forever.
    // Two safety nets:
    //   1. requestAnimationFrame'd invalidateSize on the next paint
    //      handles the "0×0 at mount, real size one frame later" case.
    //   2. A ResizeObserver redoes it on every container resize so a
    //      later orientation change / window resize / parent expand
    //      always catches up.
    const raf = requestAnimationFrame(() => {
      if (mapRef.current) mapRef.current.invalidateSize();
    });
    const ro = new ResizeObserver(() => {
      if (mapRef.current) mapRef.current.invalidateSize();
    });
    ro.observe(ref.current);

    const wpLatLngs = ws.map((w) => [w.Latitude, w.Longitude] as [number, number]);
    map.fitBounds(L.latLngBounds(wpLatLngs), { padding: [24, 24], maxZoom: 12 });

    const placeholder = L.polyline(wpLatLngs, {
      color: "#34d399",
      weight: 2,
      opacity: 0.5,
      dashArray: "4 6",
    }).addTo(map);

    wpMarkersRef.current.clear();
    for (let i = 0; i < ws.length; i++) {
      const w = ws[i];
      const origIdx = wsWithIdx[i].i;
      const isFirst = i === 0;
      const isLast = i === ws.length - 1;
      const color = isFirst ? "#10b981" : isLast ? "#f43f5e" : "#f59e0b";
      const tooltipBody = waypointTooltip(w, i, ws.length);
      const marker = L.marker([w.Latitude, w.Longitude], {
        icon: dotIcon(color, isFirst || isLast ? 12 : 10),
      })
        .addTo(map)
        .bindTooltip(tooltipBody, { sticky: true, opacity: 0.95 });
      // Marker click fires the parent's selection callback so the
      // stops-table row for this waypoint highlights in sync.
      // Closures capture origIdx + color so the click handler and
      // the external-selection effect can both swap icons.
      (marker as L.Marker & { _color?: string; _isEnd?: boolean })._color = color;
      (marker as L.Marker & { _color?: string; _isEnd?: boolean })._isEnd =
        isFirst || isLast;
      marker.on("click", () => {
        onSelectStopRef.current?.(origIdx);
      });
      wpMarkersRef.current.set(origIdx, marker);
    }

    // Dedicated layer group so the charger overlay can be swapped on
    // filter change without touching the basemap/route layers.
    const layer = L.layerGroup().addTo(map);
    chargerLayerRef.current = layer;

    const ac = new AbortController();
    void (async () => {
      await ensureConfig();
      if (ac.signal.aborted) return;
      let path: [number, number][] = wpLatLngs;
      if (valhallaBase() !== "") {
        const snapped = await valhallaMultiRoute(ws, ac.signal);
        if (snapped && snapped.length >= 2 && !ac.signal.aborted) {
          placeholder.remove();
          const line = L.polyline(snapped, {
            color: "#34d399",
            weight: 3,
            opacity: 0.85,
          }).addTo(map);
          map.fitBounds(line.getBounds(), { padding: [24, 24], maxZoom: 13 });
          path = snapped;
        }
      }
      if (ac.signal.aborted) return;
      setRoutePath(path);
    })();

    return () => {
      ac.abort();
      cancelAnimationFrame(raf);
      ro.disconnect();
      map.remove();
      mapRef.current = null;
      chargerLayerRef.current = null;
      setRoutePath([]);
      setChargerCount(null);
    };
  }, [route]);

  // Effect: external selection. When the parent (e.g. a table-row
  // click) flips selectedIdx, swap the matched marker's icon to a
  // bigger, ringed variant and pan the map onto it. Reverts the
  // previously-selected marker back to its plain icon so only one
  // is highlighted at a time.
  useEffect(() => {
    const markers = wpMarkersRef.current;
    if (markers.size === 0) return;
    for (const [idx, m] of markers.entries()) {
      const extra = m as L.Marker & { _color?: string; _isEnd?: boolean };
      const color = extra._color ?? "#f59e0b";
      const baseSize = extra._isEnd ? 12 : 10;
      const isSelected = selectedIdx === idx;
      m.setIcon(isSelected ? selectedDotIcon(color, baseSize + 6) : dotIcon(color, baseSize));
      if (isSelected) m.setZIndexOffset(1000);
      else m.setZIndexOffset(0);
    }
    if (mapRef.current && typeof selectedIdx === "number") {
      const m = markers.get(selectedIdx);
      if (m) mapRef.current.panTo(m.getLatLng(), { animate: true, duration: 0.3 });
    }
  }, [selectedIdx]);

  // Effect 2: charger overlay. Re-runs on filter or routePath change.
  // Clears + re-populates only the charger layer group — the basemap
  // and route polyline stay put, so the filter swap feels instant.
  useEffect(() => {
    if (routePath.length < 2 || !mapRef.current || !chargerLayerRef.current) return;
    const layer = chargerLayerRef.current;
    const map = mapRef.current;
    const ac = new AbortController();
    setChargersLoading(true);
    void (async () => {
      const chargers = await findChargersAlongPath(routePath, chargerFilter);
      if (ac.signal.aborted) return;
      layer.clearLayers();
      setChargerCount(chargers.length);
      setChargersLoading(false);
      for (const poi of chargers) {
        const m = L.marker([poi.lat, poi.lon], { icon: chargerDotIcon(poi.isDCFC !== false) })
          .bindPopup(chargerPopupHTML(poi, !!onAddStopRef.current));
        m.on("popupopen", (e) => {
          const btn = (e.popup.getElement() as HTMLElement | null)
            ?.querySelector<HTMLButtonElement>(".charger-add-btn");
          if (btn) {
            btn.addEventListener("click", () => {
              const label =
                poi.name ||
                poi.network ||
                `${poi.lat.toFixed(4)}, ${poi.lon.toFixed(4)}`;
              onAddStopRef.current?.({ lat: poi.lat, lon: poi.lon, label });
              map.closePopup();
            });
          }
        });
        layer.addLayer(m);
      }
    })();
    return () => {
      ac.abort();
      setChargersLoading(false);
    };
  }, [routePath, chargerFilter]);

  const filterLabels: Record<typeof chargerFilter, string> = {
    dcfc: "DCFC",
    l2: "L2",
    all: "All",
  };

  return (
    <div>
      <div className="flex items-center gap-1 mb-1">
        <span className="text-xs text-neutral-500 mr-1">Chargers:</span>
        {(["dcfc", "l2", "all"] as const).map((f) => (
          <button
            key={f}
            onClick={() => setChargerFilter(f)}
            className={`px-2 py-0.5 text-xs rounded transition-colors ${
              chargerFilter === f
                ? "bg-cyan-800 text-cyan-100"
                : "bg-neutral-800 text-neutral-400 hover:text-neutral-200"
            }`}
          >
            {filterLabels[f]}
          </button>
        ))}
        <span className="text-xs text-neutral-500 ml-1">
          {chargersLoading ? "…" : chargerCount != null ? `${chargerCount} found` : ""}
        </span>
        {chargerFilter === "all" && (
          <span className="text-xs text-neutral-600 ml-1">
            <span style={{ color: "#06b6d4" }}>●</span> DCFC &nbsp;
            <span style={{ color: "#a3e635" }}>●</span> L2
          </span>
        )}
      </div>
      <div
        ref={ref}
        className="rounded-lg overflow-hidden border border-neutral-800"
        style={{ height }}
      />
    </div>
  );
}

function waypointTooltip(w: PlannedWaypoint, i: number, total: number): string {
  const isFirst = i === 0;
  const isLast = i === total - 1;
  const heading = isFirst
    ? "Origin"
    : isLast
      ? "Destination"
      : w.Name || "Charging stop";
  const lines: string[] = [
    `<div style="font:12px/1.35 ui-sans-serif,system-ui;color:#fafafa">`,
    `<div style="color:#34d399;font-weight:600">${escapeHTML(heading)}</div>`,
  ];
  if (!isFirst && !isLast) {
    if (w.MaxPowerKW > 0) {
      lines.push(`<div>Up to ${w.MaxPowerKW.toFixed(0)} kW</div>`);
    }
    if (w.ChargeDurationSec > 0) {
      lines.push(
        `<div>Charge ${Math.round(w.ChargeDurationSec / 60)} min · ${w.ArrivalSoC.toFixed(0)}% → ${w.DepartureSoC.toFixed(0)}%</div>`,
      );
    }
    if (w.AdapterRequired) {
      lines.push(`<div style="color:#fbbf24">Tesla adapter required</div>`);
    }
  } else if (isLast) {
    lines.push(`<div>Arrive at ${w.ArrivalSoC.toFixed(0)}%</div>`);
  } else {
    lines.push(`<div>Depart at ${w.DepartureSoC.toFixed(0)}%</div>`);
  }
  lines.push(`</div>`);
  return lines.join("");
}

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function dotIcon(color: string, size = 10): L.DivIcon {
  return L.divIcon({
    className: "trip-route-dot",
    html: `<span style="display:block;width:${size}px;height:${size}px;border-radius:9999px;background:${color};border:2px solid #0a0a0a;box-shadow:0 0 0 1px ${color}88"></span>`,
    iconSize: [size + 4, size + 4],
    iconAnchor: [(size + 4) / 2, (size + 4) / 2],
  });
}

// selectedDotIcon is the larger / ringed variant used when the
// stops-table row for this waypoint is selected. The wider outer
// glow doubles as a hit target on touch devices.
function selectedDotIcon(color: string, size: number): L.DivIcon {
  return L.divIcon({
    className: "trip-route-dot-selected",
    html: `<span style="display:block;width:${size}px;height:${size}px;border-radius:9999px;background:${color};border:3px solid #fafafa;box-shadow:0 0 0 4px ${color}55, 0 0 12px ${color}99"></span>`,
    iconSize: [size + 10, size + 10],
    iconAnchor: [(size + 10) / 2, (size + 10) / 2],
  });
}

function chargerDotIcon(isDCFC = true): L.DivIcon {
  const color = isDCFC ? "#06b6d4" : "#a3e635";
  return L.divIcon({
    className: "trip-charger-dot",
    html: `<span style="display:block;width:8px;height:8px;border-radius:9999px;background:${color};border:1.5px solid #0a0a0a;opacity:0.85"></span>`,
    iconSize: [11, 11],
    iconAnchor: [5, 5],
  });
}

function chargerPopupHTML(poi: POI, addable: boolean): string {
  const name = poi.name || poi.network || "Charging station";
  const lines: string[] = [
    `<div style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:150px">`,
    `<div style="font-weight:600;margin-bottom:3px">${escapeHTML(name)}</div>`,
  ];
  if (poi.maxPowerKW && poi.maxPowerKW > 0) {
    lines.push(`<div>Up to ${poi.maxPowerKW.toFixed(0)} kW</div>`);
  }
  if (poi.network && poi.network !== name) {
    lines.push(`<div style="color:#a3a3a3">${escapeHTML(poi.network)}</div>`);
  }
  if (poi.capacity && poi.capacity > 0) {
    lines.push(`<div style="color:#a3a3a3">${poi.capacity} port${poi.capacity !== 1 ? "s" : ""}</div>`);
  }
  if (addable) {
    lines.push(
      `<button class="charger-add-btn" style="margin-top:6px;width:100%;padding:3px 8px;` +
      `background:#0e7490;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:11px">` +
      `Add as waypoint</button>`,
    );
  }
  lines.push(`</div>`);
  return lines.join("");
}

const PROTOMAPS_ATTRIB =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · <a href="https://protomaps.com">Protomaps</a>';

function addBasemap(map: L.Map) {
  const url = tilesPMTilesURL();
  if (!url) return;
  const flavor = namedFlavor("dark");
  leafletLayer({
    url,
    paintRules: paintRules(flavor),
    labelRules: labelRules(flavor, "en"),
    backgroundColor: flavor.background,
    attribution: PROTOMAPS_ATTRIB,
  }).addTo(map);
}

// valhallaMultiRoute POSTs every waypoint as a single trip and
// stitches the per-leg polyline6 shapes into one polyline. Returns
// null on any failure so the caller keeps the straight-line
// placeholder.
async function valhallaMultiRoute(
  ws: PlannedWaypoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "" || ws.length < 2) return null;
  const body = {
    locations: ws.map((w, i) => ({
      lat: w.Latitude,
      lon: w.Longitude,
      // Origin/destination are hard breaks; intermediate charging stops
      // use "through" so Valhalla doesn't fail if it can't snap a charger
      // location to the exact road network.
      type: i === 0 || i === ws.length - 1 ? "break" : "through",
    })),
    costing: "auto",
    directions_options: { units: "miles" },
  };
  try {
    const r = await fetch(`${base}/route`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      trip?: { legs?: { shape?: string }[] };
    };
    const legs = j.trip?.legs ?? [];
    if (legs.length === 0) return null;
    const out: [number, number][] = [];
    for (let i = 0; i < legs.length; i++) {
      const seg = decodePolyline6(legs[i].shape);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// decodePolyline6 mirrors the Valhalla branch of DriveMap's
// decodePolyline. Inlined here to keep TripRouteMap self-contained;
// if a third caller appears, hoist into lib/.
function decodePolyline6(s: string | undefined | null): [number, number][] {
  if (!s) return [];
  const factor = 1e-6;
  const out: [number, number][] = [];
  let lat = 0;
  let lon = 0;
  let i = 0;
  const n = s.length;
  while (i < n) {
    let result = 0;
    let shift = 0;
    let b: number;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlat = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lat += dlat;

    result = 0;
    shift = 0;
    do {
      if (i >= n) return out;
      b = s.charCodeAt(i++) - 63;
      result |= (b & 0x1f) << shift;
      shift += 5;
    } while (b >= 0x20);
    const dlon = (result & 1) !== 0 ? ~(result >> 1) : result >> 1;
    lon += dlon;

    out.push([lat * factor, lon * factor]);
  }
  return out;
}
