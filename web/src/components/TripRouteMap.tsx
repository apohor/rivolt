import { useEffect, useRef } from "react";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import { valhallaBase, ensureConfig } from "../lib/config";
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
export function TripRouteMap({
  route,
  height = 320,
}: {
  route: TripRoute;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);

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
    addCartoDark(map);

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

    // Async pass: ask Valhalla to route through every waypoint and
    // replace the dashed placeholder with the road-snapped path.
    const ac = new AbortController();
    void (async () => {
      await ensureConfig();
      if (ac.signal.aborted) return;
      if (valhallaBase() === "") return;
      const path = await valhallaMultiRoute(ws, ac.signal);
      if (!path || path.length < 2 || ac.signal.aborted) return;
      placeholder.remove();
      const line = L.polyline(path, {
        color: "#34d399",
        weight: 3,
        opacity: 0.85,
      }).addTo(map);
      map.fitBounds(line.getBounds(), { padding: [24, 24], maxZoom: 13 });
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

// addCartoDark mirrors the basemap used elsewhere — same dark
// raster split into a no-labels and labels-only layer so place
// names stay legible above the polyline.
const CARTO_ATTRIB =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>';
function addCartoDark(map: L.Map) {
  L.tileLayer(
    "https://{s}.basemaps.cartocdn.com/dark_nolabels/{z}/{x}/{y}{r}.png",
    {
      subdomains: "abcd",
      maxZoom: 19,
      attribution: CARTO_ATTRIB,
      pane: "tilePane",
    },
  ).addTo(map);
  L.tileLayer(
    "https://{s}.basemaps.cartocdn.com/dark_only_labels/{z}/{x}/{y}{r}.png",
    {
      subdomains: "abcd",
      maxZoom: 19,
      attribution: "",
      pane: "shadowPane",
    },
  ).addTo(map);
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
    locations: ws.map((w) => ({ lat: w.Latitude, lon: w.Longitude, type: "break" })),
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
