// DriveMap renders the drive route on an OpenStreetMap basemap.
//
// We use Leaflet directly (not react-leaflet's MapContainer abstractions)
// to keep the lifecycle explicit and fit-bounds logic straightforward.
// The tile layer points at the public OSM tile server, which is fine for
// a personal tool but you'd swap in a self-hosted or paid provider for
// higher traffic.

import { useEffect, useRef } from "react";
import L from "leaflet";
import "leaflet/dist/leaflet.css";

// Route raw GPS samples along actual roads using the public OSRM demo's
// /route endpoint. Raw telemetry samples are typically 20–60s apart;
// drawing straight lines between them "cuts corners" and makes the
// trace look like it's in the middle of a field. /route returns a
// turn-by-turn driving path between successive waypoints, which gives
// us a road-following polyline.
//
// We use /route rather than /match because /match requires tightly-
// spaced trace points with timestamps to behave well on sparse GPS
// (our samples can be a minute apart). /route just connects the dots
// with real road segments, which is visually what we want here.
//
// Notes:
// - router.project-osrm.org's /route accepts up to 100 waypoints. We
//   downsample if needed and include start/end pinned points so the
//   path connects to the markers.
// - Request is best-effort: on failure we keep the raw polyline.
// - For production you'd host your own OSRM or use Mapbox Directions.
async function snapToRoads(
  points: Point[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  if (points.length < 2) return null;
  const MAX = 90;
  const step = Math.max(1, Math.ceil(points.length / MAX));
  const sampled = points.filter((_, i) => i % step === 0);
  if (sampled[sampled.length - 1] !== points[points.length - 1]) {
    sampled.push(points[points.length - 1]);
  }
  const coords = sampled.map((p) => `${p.lon},${p.lat}`).join(";");
  const url =
    `https://router.project-osrm.org/route/v1/driving/${coords}` +
    `?geometries=geojson&overview=full`;
  try {
    const r = await fetch(url, { signal });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      routes?: { geometry: { coordinates: [number, number][] } }[];
    };
    const route = j.routes?.[0];
    if (!route) return null;
    const out: [number, number][] = route.geometry.coordinates.map(
      ([lon, lat]) => [lat, lon],
    );
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// Leaflet ships broken marker icon URLs when bundled. Replace them with
// an inline DOM marker so we don't need bundler asset plumbing.
// Emerald for start, rose for end, amber for charge. A thin dark ring
// keeps the dot legible against both the polyline and the basemap.
function circleIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "rivolt-map-marker",
    html: `<span style="display:block;width:14px;height:14px;border-radius:9999px;background:${color};border:2px solid #0a0a0a;box-shadow:0 0 0 2px ${color}33;"></span>`,
    iconSize: [14, 14],
    iconAnchor: [7, 7],
  });
}

type Point = { lat: number; lon: number };

export function DriveMap({
  points,
  start,
  end,
  height = 320,
}: {
  points: Point[];
  start?: Point;
  end?: Point;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<L.Map | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    const valid = points.filter(
      (p) =>
        Number.isFinite(p.lat) &&
        Number.isFinite(p.lon) &&
        (p.lat !== 0 || p.lon !== 0),
    );
    const fallback: Point | undefined = start ?? end ?? valid[0];
    if (valid.length === 0 && !fallback) return;

    const center: [number, number] = fallback
      ? [fallback.lat, fallback.lon]
      : [valid[0].lat, valid[0].lon];

    const map = L.map(ref.current, {
      zoomControl: true,
      attributionControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
    }).setView(center, 13);
    mapRef.current = map;

    // Click to enable wheel zoom; blur (mouseout) disables it again so
    // the page keeps scrolling normally over the map.
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());

    L.tileLayer(
      "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
      {
        maxZoom: 20,
        subdomains: "abcd",
        attribution:
          '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>',
      },
    ).addTo(map);

    // Pick start/end markers. Prefer caller-supplied start/end (the
    // page can derive these from parked samples flanking the drive,
    // which is more accurate than any in-drive GPS fix because
    // telemetry frequently misses the first minute of a trip).
    // Fall back to the polyline endpoints when no hint is given.
    const lineStart: Point | undefined = start ?? valid[0];
    const lineEnd: Point | undefined = end ?? valid[valid.length - 1];

    // Round-trip detection: GPS samples jitter by a few meters even
    // when parked, so strict equality never collapses. Use a ~50 m
    // threshold (≈0.0005° at mid latitudes) so any drive that begins
    // and ends in the same parking spot shows a single green "home"
    // pin instead of two overlapping markers.
    const sameSpot =
      !!lineStart &&
      !!lineEnd &&
      Math.abs(lineStart.lat - lineEnd.lat) < 0.0005 &&
      Math.abs(lineStart.lon - lineEnd.lon) < 0.0005;

    // Draw the trace. When we have a reliable "home" start/end from a
    // parked sample, extend the polyline out to it so the route visibly
    // connects to the pins (otherwise there's a dangling gap between
    // the first in-drive GPS fix and the start marker).
    const latlngs: [number, number][] = [];
    if (start) latlngs.push([start.lat, start.lon]);
    for (const p of valid) latlngs.push([p.lat, p.lon]);
    if (end && !sameSpot) latlngs.push([end.lat, end.lon]);
    let line: L.Polyline | null = null;
    if (latlngs.length > 1) {
      line = L.polyline(latlngs, {
        color: "#10b981",
        weight: 3,
        opacity: 0.9,
      }).addTo(map);
      map.fitBounds(line.getBounds(), { padding: [20, 20] });
    }

    // Best-effort: replace the straight-line polyline with a road-snapped
    // geometry from OSRM. If the request fails (rate limit, offline,
    // non-drivable terrain) we keep the raw trace. The abort controller
    // cancels the in-flight request if the component unmounts or props
    // change before OSRM responds.
    const ac = new AbortController();
    const tracePoints: Point[] = [
      ...(start ? [start] : []),
      ...valid,
      ...(end && !sameSpot ? [end] : []),
    ];
    snapToRoads(tracePoints, ac.signal).then((matched) => {
      if (!matched || !mapRef.current) return;
      if (line) line.remove();
      const snapped = L.polyline(matched, {
        color: "#10b981",
        weight: 3,
        opacity: 0.95,
      }).addTo(map);
      map.fitBounds(snapped.getBounds(), { padding: [20, 20] });
    });

    if (lineStart) {
      L.marker([lineStart.lat, lineStart.lon], {
        icon: circleIcon("#10b981"),
        zIndexOffset: 1000,
      })
        .addTo(map)
        .bindTooltip(sameSpot ? "Start / End" : "Start", { direction: "top" });
    }
    if (lineEnd && !sameSpot) {
      L.marker([lineEnd.lat, lineEnd.lon], {
        icon: circleIcon("#f43f5e"),
        zIndexOffset: 1000,
      })
        .addTo(map)
        .bindTooltip("End", { direction: "top" });
    }

    // Leaflet reads the container size on init; if we mount inside a
    // freshly-revealed card the size is wrong until the next tick.
    // Re-invalidate after layout settles, and again when the window
    // resizes, so the tile pane always covers the full card.
    const invalidate = () => {
      map.invalidateSize();
      if (latlngs.length > 1) {
        map.fitBounds(L.latLngBounds(latlngs), { padding: [20, 20] });
      }
    };
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);

    return () => {
      ac.abort();
      cancelAnimationFrame(rAF);
      ro.disconnect();
      map.remove();
      mapRef.current = null;
    };
    // points is an array derived upstream; re-run only when identity changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points, start?.lat, start?.lon, end?.lat, end?.lon]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// ChargeMap is a single-pin variant for charge sessions.
export function ChargeMap({
  lat,
  lon,
  height = 240,
}: {
  lat: number;
  lon: number;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    if (!Number.isFinite(lat) || !Number.isFinite(lon) || (lat === 0 && lon === 0)) {
      return;
    }
    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
    }).setView([lat, lon], 15);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    L.tileLayer(
      "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
      {
        maxZoom: 20,
        subdomains: "abcd",
        attribution:
          '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>',
      },
    ).addTo(map);
    L.marker([lat, lon], {
      icon: circleIcon("#f59e0b"),
      zIndexOffset: 1000,
    })
      .addTo(map)
      .bindTooltip("Charge location", { direction: "top" });
    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelAnimationFrame(rAF);
      ro.disconnect();
      map.remove();
    };
  }, [lat, lon]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}
