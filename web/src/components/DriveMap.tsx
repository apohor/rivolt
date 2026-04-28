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

// Snap raw GPS samples to actual roads using OSRM's map-matching
// endpoint (/match). Map-matching is the right primitive for this:
// it takes a noisy GPS trace plus timestamps and returns the road
// geometry the vehicle most likely traveled, given the kinematic
// constraints of the road network.
//
// Why /match and not /route:
//   /route returns the *cheapest drivable path* between a list of
//   waypoints, ignoring everything between them. With sparse or
//   jittery samples — e.g., low-speed parking-lot maneuvers where
//   GPS lands on the wrong side of a building — /route happily
//   picks a different path than was actually driven, often 20–30%
//   longer. We saw exactly this on a real 2.5 mi drive that /route
//   stretched into a 3.1 mi reroute.
//
//   /match instead treats the trace as evidence: it walks the road
//   graph using a Hidden Markov Model, weighted by point-to-road
//   distance and travel-time plausibility. The result hugs the
//   actual driven roads even when individual fixes are noisy.
//
// Why we chunk:
//   The public OSRM demo caps /match at 9 trace coordinates per
//   request — far below the 100-coord cap on /route. To use /match
//   on a multi-mile drive we walk the trace in overlapping chunks
//   of CHUNK_SIZE points (overlap of 1 keeps adjacent chunks
//   geometrically continuous). Self-hosted OSRM lifts this cap, at
//   which point the chunking is harmless overhead.
//
// Trace requirements & tradeoffs:
//   - We downsample to MAX_TRACE so the request count stays bounded
//     for long drives (otherwise rate limits will start denying us).
//   - `tidy=true` lets OSRM drop pathological points itself, which
//     materially improves match quality on stop-and-go traces.
//   - /match can split a chunk into multiple `matchings` if it
//     loses confidence (signal gap, U-turn, off-road segment); we
//     concatenate their geometries in order.
//   - On any chunk failure we fall through to a single /route call,
//     and on /route failure to the raw straight-line polyline.
//
// For production scale you'd self-host OSRM (or use Mapbox/Valhalla)
// instead of the public demo server, which is rate-limited.
type SnapPoint = { lat: number; lon: number; t?: number };

const MATCH_CHUNK_SIZE = 9; // public OSRM demo cap per /match request
const MATCH_CHUNK_OVERLAP = 1; // shared anchor point between chunks
const MAX_TRACE = 49; // = 6 × (9 − 1) + 1, i.e. ≤ 6 /match calls

async function matchChunk(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  if (pts.length < 2) return null;
  const coords = pts.map((p) => `${p.lon},${p.lat}`).join(";");
  const url =
    `https://router.project-osrm.org/match/v1/driving/${coords}` +
    `?geometries=geojson&overview=full&tidy=true`;
  try {
    const r = await fetch(url, { signal });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      code?: string;
      matchings?: { geometry: { coordinates: [number, number][] } }[];
    };
    if (j.code !== "Ok" || !j.matchings?.length) return null;
    // OSRM splits the response into multiple matchings when it
    // can't confidently connect the whole trace as one path
    // (typical at parking lots / off-graph drift). Naïvely
    // concatenating draws a straight chord between matching N's
    // end and matching N+1's start — visibly cuts through
    // buildings. Bridge each gap with a /route call so the
    // polyline follows the road network all the way through.
    const out: [number, number][] = [];
    for (let i = 0; i < j.matchings.length; i++) {
      const seg = j.matchings[i].geometry.coordinates.map(
        ([lon, lat]) => [lat, lon] as [number, number],
      );
      if (i === 0) {
        out.push(...seg);
        continue;
      }
      const from = out[out.length - 1];
      const to = seg[0];
      const bridge = await routeAll(
        [
          { lat: from[0], lon: from[1] },
          { lat: to[0], lon: to[1] },
        ],
        signal,
      );
      if (bridge && bridge.length > 1) {
        // bridge[0] === from (already in out); bridge.at(-1) === to
        // (also seg[0]) — drop both endpoints to avoid duplicates.
        out.push(...bridge.slice(1, -1));
      }
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

async function routeAll(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  if (pts.length < 2) return null;
  const coords = pts.map((p) => `${p.lon},${p.lat}`).join(";");
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

async function snapToRoads(
  points: SnapPoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  if (points.length < 2) return null;

  const step = Math.max(1, Math.ceil(points.length / MAX_TRACE));
  const sampled = points.filter((_, i) => i % step === 0);
  if (sampled[sampled.length - 1] !== points[points.length - 1]) {
    sampled.push(points[points.length - 1]);
  }

  // Walk overlapping chunks through /match. The first point of each
  // subsequent chunk duplicates the previous chunk's last point so
  // the matched geometries connect without a visible seam — we drop
  // that duplicated head when stitching.
  const stride = MATCH_CHUNK_SIZE - MATCH_CHUNK_OVERLAP;
  const matched: [number, number][] = [];
  for (let i = 0; i < sampled.length - 1; i += stride) {
    if (signal.aborted) return null;
    const chunk = sampled.slice(i, i + MATCH_CHUNK_SIZE);
    if (chunk.length < 2) break;
    const m = await matchChunk(chunk, signal);
    if (!m) {
      // /match gave up on this chunk. Bail out of the chunked path
      // entirely and try a single /route over the whole trace —
      // less faithful to the actual driven path, but better than
      // returning a partial polyline.
      return await routeAll(sampled, signal);
    }
    if (matched.length > 0 && m.length > 0) m.shift();
    matched.push(...m);
    if (i + MATCH_CHUNK_SIZE >= sampled.length) break;
  }
  if (matched.length > 1) return matched;

  // Final fallback: /route, then raw.
  return await routeAll(sampled, signal);
}

// Tile config shared by both maps. CARTO's dark basemap split into a
// no-labels layer (z-index below the route polyline) and a labels-only
// layer (z-index above the route), so place names stay legible without
// being cut by the line.
const CARTO_ATTRIB =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · © <a href="https://carto.com/attributions">CARTO</a>';
function addCartoDark(map: L.Map) {
  L.tileLayer(
    "https://{s}.basemaps.cartocdn.com/dark_nolabels/{z}/{x}/{y}{r}.png",
    {
      maxZoom: 20,
      subdomains: "abcd",
      attribution: CARTO_ATTRIB,
      className: "rivolt-tiles-base",
    },
  ).addTo(map);
  L.tileLayer(
    "https://{s}.basemaps.cartocdn.com/dark_only_labels/{z}/{x}/{y}{r}.png",
    {
      maxZoom: 20,
      subdomains: "abcd",
      pane: "markerPane",
      attribution: "",
    },
  ).addTo(map);
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

// drawRoute renders the polyline as a wide low-opacity glow underneath
// a crisp main line, which reads much better on the dark basemap than
// a single flat stroke.
function drawRoute(map: L.Map, latlngs: [number, number][]): L.LayerGroup {
  const group = L.layerGroup();
  L.polyline(latlngs, {
    color: "#10b981",
    weight: 9,
    opacity: 0.18,
    lineCap: "round",
    lineJoin: "round",
  }).addTo(group);
  L.polyline(latlngs, {
    color: "#34d399",
    weight: 3,
    opacity: 0.95,
    lineCap: "round",
    lineJoin: "round",
  }).addTo(group);
  group.addTo(map);
  return group;
}

type Point = { lat: number; lon: number; t?: number };

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
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView(center, 13);
    mapRef.current = map;

    // Click to enable wheel zoom; mouseout disables it again so the
    // page keeps scrolling normally over the map.
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());

    addCartoDark(map);

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
    let line: L.LayerGroup | null = null;
    if (latlngs.length > 1) {
      line = drawRoute(map, latlngs);
      map.fitBounds(L.latLngBounds(latlngs), { padding: [20, 20] });
    }

    // Best-effort: replace the straight-line polyline with a road-snapped
    // geometry from OSRM. If the request fails (rate limit, offline,
    // non-drivable terrain) we keep the raw trace. The abort controller
    // cancels the in-flight request if the component unmounts or props
    // change before OSRM responds.
    const ac = new AbortController();
    // Synthesize bracketing timestamps for the parked start/end pins
    // so the trace stays monotonic. We anchor them ~60 s outside the
    // first/last in-drive sample, which mirrors how Rivian's
    // telemetry typically misses the very start and end of a trip.
    const firstT = valid.find((p) => Number.isFinite(p.t))?.t;
    const lastT = [...valid]
      .reverse()
      .find((p) => Number.isFinite(p.t))?.t;
    const startT = Number.isFinite(firstT)
      ? (firstT as number) - 60
      : undefined;
    const endT = Number.isFinite(lastT) ? (lastT as number) + 60 : undefined;
    const tracePoints: Point[] = [
      ...(start ? [{ ...start, t: startT }] : []),
      ...valid,
      ...(end && !sameSpot ? [{ ...end, t: endT }] : []),
    ];
    snapToRoads(tracePoints, ac.signal).then((matched) => {
      if (!matched || !mapRef.current) return;
      if (line) line.remove();
      const snapped = drawRoute(map, matched);
      map.fitBounds(L.latLngBounds(matched), { padding: [20, 20] });
      line = snapped;
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
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([lat, lon], 15);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addCartoDark(map);
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
