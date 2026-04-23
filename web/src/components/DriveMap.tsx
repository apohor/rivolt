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

// Leaflet ships broken marker icon URLs when bundled. Replace them with
// an inline SVG data URL so the default <Marker> works without bundler
// asset plumbing. Emerald for start, rose for end.
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
    }).setView(center, 13);
    mapRef.current = map;

    L.tileLayer(
      "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
      {
        maxZoom: 20,
        subdomains: "abcd",
        attribution:
          '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>',
      },
    ).addTo(map);

    if (valid.length > 1) {
      const latlngs = valid.map((p) => [p.lat, p.lon]) as [number, number][];
      const line = L.polyline(latlngs, {
        color: "#10b981",
        weight: 3,
        opacity: 0.9,
      }).addTo(map);
      map.fitBounds(line.getBounds(), { padding: [20, 20] });
    }

    if (start && Number.isFinite(start.lat)) {
      L.marker([start.lat, start.lon], { icon: circleIcon("#10b981") })
        .addTo(map)
        .bindTooltip("Start", { direction: "top" });
    }
    if (end && Number.isFinite(end.lat) && (end.lat !== 0 || end.lon !== 0)) {
      L.marker([end.lat, end.lon], { icon: circleIcon("#f43f5e") })
        .addTo(map)
        .bindTooltip("End", { direction: "top" });
    }

    // Leaflet reads the container size on init; if we mount inside a
    // freshly-revealed card the size is wrong until the next tick.
    // Re-invalidate after layout settles, and again when the window
    // resizes, so the tile pane always covers the full card.
    const invalidate = () => {
      map.invalidateSize();
      if (valid.length > 1) {
        const latlngs = valid.map((p) => [p.lat, p.lon]) as [number, number][];
        map.fitBounds(L.latLngBounds(latlngs), { padding: [20, 20] });
      }
    };
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);

    return () => {
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
    }).setView([lat, lon], 15);
    L.tileLayer(
      "https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png",
      {
        maxZoom: 20,
        subdomains: "abcd",
        attribution:
          '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>',
      },
    ).addTo(map);
    L.marker([lat, lon], { icon: circleIcon("#f59e0b") })
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
