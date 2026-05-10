import { useEffect, useRef } from "react";
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
}: {
  route: TripRoute;
  height?: number;
  onAddStop?: AddStop;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  // Ref so the async effect can call the latest callback without
  // being in the dep array (which would re-mount the map on every render).
  const onAddStopRef = useRef<AddStop | undefined>(onAddStop);
  useEffect(() => { onAddStopRef.current = onAddStop; }, [onAddStop]);

  useEffect(() => {
    if (!ref.current) return;
    const ws = route.Waypoints.filter(
      (w) =>
        Number.isFinite(w.Latitude) &&
        Number.isFinite(w.Longitude) &&
        !(w.Latitude === 0 && w.Longitude === 0),
    );
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
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    // Initial fit to the waypoint bounds; the road-snapped path
    // refines this once it lands.
    const wpLatLngs = ws.map((w) => [w.Latitude, w.Longitude] as [number, number]);
    map.fitBounds(L.latLngBounds(wpLatLngs), { padding: [24, 24], maxZoom: 12 });

    // Straight-line placeholder so the polyline shows up immediately
    // even before the Valhalla call lands.
    const placeholder = L.polyline(wpLatLngs, {
      color: "#34d399",
      weight: 2,
      opacity: 0.5,
      dashArray: "4 6",
    }).addTo(map);

    // Markers, in waypoint order. Origin / destination get distinct
    // colors so the direction reads at a glance; intermediate stops
    // are amber and labeled with the charger name + max kW.
    for (let i = 0; i < ws.length; i++) {
      const w = ws[i];
      const isFirst = i === 0;
      const isLast = i === ws.length - 1;
      const color = isFirst ? "#10b981" : isLast ? "#f43f5e" : "#f59e0b";
      const tooltipBody = waypointTooltip(w, i, ws.length);
      L.marker([w.Latitude, w.Longitude], {
        icon: dotIcon(color, isFirst || isLast ? 12 : 10),
      })
        .addTo(map)
        .bindTooltip(tooltipBody, { sticky: true, opacity: 0.95 });
    }

    // Async pass: road-snap via Valhalla, then overlay nearby DCFC chargers.
    const ac = new AbortController();
    void (async () => {
      await ensureConfig();
      if (ac.signal.aborted) return;

      // Use Valhalla road-snapped path when available; fall back to
      // straight-line waypoint path for the charger corridor query.
      let routePath: [number, number][] = wpLatLngs;
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
          routePath = snapped;
        }
      }
      if (ac.signal.aborted) return;

      // Overlay fast chargers (≥ 50 kW) near the route corridor.
      const chargers = await findChargersAlongPath(routePath);
      if (ac.signal.aborted) return;
      for (const poi of chargers) {
        const m = L.marker([poi.lat, poi.lon], { icon: chargerDotIcon() })
          .addTo(map)
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
      }
    })();

    return () => {
      ac.abort();
      map.remove();
    };
  }, [route]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
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

function chargerDotIcon(): L.DivIcon {
  return L.divIcon({
    className: "trip-charger-dot",
    html: `<span style="display:block;width:8px;height:8px;border-radius:9999px;background:#06b6d4;border:1.5px solid #0a0a0a;opacity:0.85"></span>`,
    iconSize: [11, 11],
    iconAnchor: [5, 5],
  });
}

function chargerPopupHTML(poi: POI, addable: boolean): string {
  const name = poi.name || poi.network || "Charging station";
  const lines: string[] = [
    `<div style="font:12px/1.4 ui-sans-serif,system-ui;color:#111;min-width:150px">`,
    `<div style="font-weight:600;margin-bottom:3px">${escapeHTML(name)}</div>`,
  ];
  if (poi.maxPowerKW && poi.maxPowerKW > 0) {
    lines.push(`<div>Up to ${poi.maxPowerKW.toFixed(0)} kW</div>`);
  }
  if (poi.network && poi.network !== name) {
    lines.push(`<div style="color:#555">${escapeHTML(poi.network)}</div>`);
  }
  if (poi.capacity && poi.capacity > 0) {
    lines.push(`<div style="color:#555">${poi.capacity} port${poi.capacity !== 1 ? "s" : ""}</div>`);
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
