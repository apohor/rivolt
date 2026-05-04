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
type SnapPoint = { lat: number; lon: number; t?: number; s?: number };

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

// Speed-bucket palette. Reads roughly as: gray crawl → cyan city →
// emerald suburban → amber highway → rose interstate. Tuned for
// US units (mph) with thresholds at common speed-limit boundaries.
const SPEED_BUCKETS: { max: number; color: string; label: string }[] = [
  { max: 5, color: "#6b7280", label: "<5" },
  { max: 25, color: "#06b6d4", label: "5–25" },
  { max: 50, color: "#34d399", label: "25–50" },
  { max: 65, color: "#f59e0b", label: "50–65" },
  { max: Infinity, color: "#f43f5e", label: "65+" },
];

function speedColor(mph: number | undefined): string {
  if (mph == null || !Number.isFinite(mph)) return "#34d399";
  for (const b of SPEED_BUCKETS) if (mph < b.max) return b.color;
  return SPEED_BUCKETS[SPEED_BUCKETS.length - 1].color;
}

// drawRoute renders the polyline as a wide low-opacity glow underneath
// a crisp main line. When per-point speeds are supplied we segment the
// crisp line by speed bucket so the route reads as a heatmap of pace —
// gray for parking-lot crawls, rose for interstate. The glow stays a
// single emerald wash so the line still reads as one continuous path
// on the dark basemap. When speeds are absent we fall back to the
// uniform emerald stroke.
function drawRoute(
  map: L.Map,
  latlngs: [number, number][],
  speeds?: (number | undefined)[],
): L.LayerGroup {
  const group = L.layerGroup();
  L.polyline(latlngs, {
    color: "#10b981",
    weight: 9,
    opacity: 0.18,
    lineCap: "round",
    lineJoin: "round",
  }).addTo(group);
  const hasSpeeds =
    !!speeds &&
    speeds.length === latlngs.length &&
    speeds.some((s) => s != null && Number.isFinite(s));
  if (!hasSpeeds) {
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
  // Walk the line and emit a sub-polyline whenever the speed bucket
  // changes. Each segment shares an anchor point with the next so
  // the line stays visually continuous (no gaps at transitions).
  let segStart = 0;
  let segColor = speedColor(speeds![0]);
  for (let i = 1; i < latlngs.length; i++) {
    const c = speedColor(speeds![i]);
    if (c !== segColor) {
      L.polyline(latlngs.slice(segStart, i + 1), {
        color: segColor,
        weight: 3,
        opacity: 0.95,
        lineCap: "round",
        lineJoin: "round",
      }).addTo(group);
      segStart = i;
      segColor = c;
    }
  }
  L.polyline(latlngs.slice(segStart), {
    color: segColor,
    weight: 3,
    opacity: 0.95,
    lineCap: "round",
    lineJoin: "round",
  }).addTo(group);
  group.addTo(map);
  return group;
}

// addSpeedLegend adds a compact bottom-left swatch row showing what
// each color band means. Pure DOM control, no Leaflet pane reflow.
function addSpeedLegend(map: L.Map): L.Control {
  const ctl = new L.Control({ position: "bottomleft" });
  ctl.onAdd = () => {
    const div = L.DomUtil.create("div", "rivolt-speed-legend");
    div.style.cssText =
      "background:rgba(10,10,10,0.78);border:1px solid #262626;" +
      "border-radius:6px;padding:4px 6px;display:flex;gap:6px;" +
      "align-items:center;font:10px/1.1 ui-sans-serif,system-ui;" +
      "color:#d4d4d4;backdrop-filter:blur(2px);";
    div.innerHTML =
      '<span style="color:#737373;margin-right:2px">mph</span>' +
      SPEED_BUCKETS.map(
        (b) =>
          `<span style="display:inline-flex;align-items:center;gap:3px">` +
          `<span style="display:inline-block;width:10px;height:3px;background:${b.color};border-radius:2px"></span>` +
          `${b.label}</span>`,
      ).join("");
    L.DomEvent.disableClickPropagation(div);
    return div;
  };
  ctl.addTo(map);
  return ctl;
}

// stalenessBadge renders a top-right amber pill warning that the GPS
// fix backing the visible position is older than wall-clock would
// suggest. The pill is purely informational and click-through to the
// map underneath. Shown only when fix age crosses a 5-minute
// threshold; below that, normal poll cadence jitter is uninteresting.
function stalenessBadge(map: L.Map, fixAgeSeconds: number): L.Control {
  const ctl = new L.Control({ position: "topright" });
  ctl.onAdd = () => {
    const div = L.DomUtil.create("div", "rivolt-staleness-badge");
    const ageMin = Math.round(fixAgeSeconds / 60);
    // Format as "Hh Mm" past 60 min so an hours-long freeze stays
    // legible rather than reading "147 min".
    const label =
      ageMin >= 60
        ? `${Math.floor(ageMin / 60)}h ${ageMin % 60}m`
        : `${ageMin} min`;
    div.style.cssText =
      "background:rgba(120,53,15,0.85);border:1px solid #b45309;" +
      "border-radius:6px;padding:3px 7px;display:inline-flex;gap:4px;" +
      "align-items:center;font:11px/1.1 ui-sans-serif,system-ui;" +
      "color:#fde68a;backdrop-filter:blur(2px);" +
      "box-shadow:0 1px 2px rgba(0,0,0,0.4);";
    div.title =
      "The GNSS module reported this fix " +
      label +
      " before the wall-clock timestamp. The marker may be stale.";
    div.innerHTML =
      '<span style="color:#fbbf24">⚠</span>' +
      `<span>GPS fix ${label} stale</span>`;
    L.DomEvent.disableClickPropagation(div);
    return div;
  };
  ctl.addTo(map);
  return ctl;
}

type Point = { lat: number; lon: number; t?: number; s?: number };

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
  // Controlled cursor in unix seconds. When set, a small accent
  // marker is rendered on the polyline at the sample whose
  // timestamp is closest to this value. Used to keep the route in
  // sync with hover on the speed/battery charts.
  cursorTime?: number | null;
  // Hover/leave callback. Fires with the unix-seconds timestamp of
  // the polyline sample nearest the pointer (only when the pointer
  // is close to the trace, in pixel space) and with `null` on
  // mouseout.
  onCursorChange?: (t: number | null) => void;
  // Worst observed GNSS fix age across the drive's samples (seconds).
  // When ≥ 5 min, a small "GPS fix N min stale" badge is overlaid in
  // the top-right corner so the user knows the polyline may be
  // built from frozen-fix coordinates.
  fixAgeSeconds?: number | null;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<L.Map | null>(null);
  // Track which time samples are on the polyline so the hover and
  // cursor effects can both binary/linear-search the same array.
  const tracePointsRef = useRef<Point[]>([]);
  // Stable ref to the latest onCursorChange callback so we don't
  // tear down and rebuild the map every time the parent re-renders
  // with a new closure.
  const onCursorChangeRef = useRef(onCursorChange);
  onCursorChangeRef.current = onCursorChange;
  // Layer for the synced cursor marker (managed by a separate
  // effect so cursor updates never trigger a full map rebuild).
  const cursorLayerRef = useRef<L.CircleMarker | null>(null);

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
    const speeds: (number | undefined)[] = [];
    if (start) {
      latlngs.push([start.lat, start.lon]);
      speeds.push(0); // parked
    }
    for (const p of valid) {
      latlngs.push([p.lat, p.lon]);
      speeds.push(p.s);
    }
    if (end && !sameSpot) {
      latlngs.push([end.lat, end.lon]);
      speeds.push(0); // parked
    }
    let line: L.LayerGroup | null = null;
    if (latlngs.length > 1) {
      line = drawRoute(map, latlngs, speeds);
      map.fitBounds(L.latLngBounds(latlngs), { padding: [20, 20] });
    }
    // Show the speed legend whenever we have any per-point speed data.
    const hasSpeed = speeds.some((s) => s != null && Number.isFinite(s));
    const legend = hasSpeed ? addSpeedLegend(map) : null;

    // Optional GPS staleness badge for drives. Mounted alongside
    // (not instead of) the speed legend; positions don't collide.
    const STALE_THRESHOLD_S = 5 * 60;
    const stale =
      typeof fixAgeSeconds === "number" &&
      Number.isFinite(fixAgeSeconds) &&
      fixAgeSeconds >= STALE_THRESHOLD_S
        ? stalenessBadge(map, fixAgeSeconds)
        : null;

    // Best-effort: replace the straight-line polyline with a road-snapped
    // geometry from OSRM. If the request fails (rate limit, offline,
    // non-drivable terrain) we keep the raw trace. The abort controller
    // cancels the in-flight request if the component unmounts or props
    // change before OSRM responds.
    //
    // We only invoke /match when the GPS sample density is too sparse
    // for the raw trace to look like a road (e.g. a drive with only
    // a handful of mid-trip fixes). Rivian normally streams 3–5 s
    // samples that already lie within ~3 m of the actual road, and
    // running them through OSRM's HMM downsamples + sometimes snaps
    // the trace to a parallel arterial (Reagan Blvd in our test
    // round trip) instead of the residential streets actually driven.
    // For dense traces, the raw polyline is more faithful than any
    // /match output we can get out of the public OSRM demo (capped
    // at 9 coordinates per request).
    const ac = new AbortController();
    const SPARSE_MIN_GAP_M = 200; // average spacing that justifies /match
    let avgGap = 0;
    if (valid.length >= 2) {
      let total = 0;
      for (let i = 1; i < valid.length; i++) {
        const a = valid[i - 1];
        const b = valid[i];
        const dy = (b.lat - a.lat) * 111111;
        const dx =
          (b.lon - a.lon) * 111111 * Math.cos((a.lat * Math.PI) / 180);
        total += Math.hypot(dx, dy);
      }
      avgGap = total / (valid.length - 1);
    }
    const useOsrm = avgGap >= SPARSE_MIN_GAP_M;
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
    if (useOsrm) {
      snapToRoads(tracePoints, ac.signal).then((matched) => {
        if (!matched || !mapRef.current) return;
        if (line) line.remove();
        // OSRM returns a denser geometry than the input trace, so
        // we no longer have a 1:1 speed mapping. Project each
        // returned coord to its nearest input point and steal
        // that point's speed bucket. Cheap O(n*m) loop — m is
        // capped at MAX_TRACE so this stays fast.
        const matchedSpeeds: (number | undefined)[] = matched.map(
          ([lat, lon]) => {
            let bestI = 0;
            let bestD = Infinity;
            for (let i = 0; i < tracePoints.length; i++) {
              const p = tracePoints[i];
              const dy = (p.lat - lat) * 111111;
              const dx =
                (p.lon - lon) * 111111 * Math.cos((lat * Math.PI) / 180);
              const d = dx * dx + dy * dy;
              if (d < bestD) {
                bestD = d;
                bestI = i;
              }
            }
            return tracePoints[bestI].s;
          },
        );
        const snapped = drawRoute(map, matched, matchedSpeeds);
        map.fitBounds(L.latLngBounds(matched), { padding: [20, 20] });
        line = snapped;
      });
    }

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

    // Hover→time emission. We project each timestamped sample to
    // pixel space on every move and emit the time of the nearest
    // one, but only when the pointer is reasonably close (≤ 28 px)
    // to the polyline. This avoids the cursor jumping to a point
    // hundreds of meters away just because the user is panning the
    // map. Re-projects on every event, which is fine for the
    // hundreds of points in a typical drive.
    const timed = valid.filter((p) => Number.isFinite(p.t)) as Required<Point>[];
    tracePointsRef.current = timed;
    if (timed.length > 0) {
      const HOVER_PX = 28;
      map.on("mousemove", (ev: L.LeafletMouseEvent) => {
        const cb = onCursorChangeRef.current;
        if (!cb) return;
        const px = map.latLngToContainerPoint(ev.latlng);
        let bestI = -1;
        let bestD2 = HOVER_PX * HOVER_PX;
        for (let i = 0; i < timed.length; i++) {
          const p = timed[i];
          const q = map.latLngToContainerPoint([p.lat, p.lon]);
          const dx = q.x - px.x;
          const dy = q.y - px.y;
          const d2 = dx * dx + dy * dy;
          if (d2 < bestD2) {
            bestD2 = d2;
            bestI = i;
          }
        }
        if (bestI >= 0) cb(timed[bestI].t);
      });
      map.on("mouseout", () => {
        onCursorChangeRef.current?.(null);
      });
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
      if (legend) legend.remove();
      if (stale) stale.remove();
      map.remove();
      mapRef.current = null;
    };
    // points is an array derived upstream; re-run only when identity changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points, start?.lat, start?.lon, end?.lat, end?.lon, fixAgeSeconds]);

  // Sync the cursor marker on cursorTime changes without rebuilding
  // the map. We binary/linear-search the timed trace for the closest
  // sample by t and reposition (or create) a small accent marker.
  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;
    const timed = tracePointsRef.current;
    if (cursorTime == null || !Number.isFinite(cursorTime) || timed.length === 0) {
      if (cursorLayerRef.current) {
        cursorLayerRef.current.remove();
        cursorLayerRef.current = null;
      }
      return;
    }
    let best = timed[0];
    let bestD = Math.abs((timed[0].t ?? 0) - cursorTime);
    for (let i = 1; i < timed.length; i++) {
      const d = Math.abs((timed[i].t ?? 0) - cursorTime);
      if (d < bestD) {
        bestD = d;
        best = timed[i];
      }
    }
    if (cursorLayerRef.current) {
      cursorLayerRef.current.setLatLng([best.lat, best.lon]);
    } else {
      cursorLayerRef.current = L.circleMarker([best.lat, best.lon], {
        radius: 6,
        color: "#0a0a0a",
        weight: 2,
        fillColor: "#fbbf24",
        fillOpacity: 1,
        interactive: false,
      }).addTo(map);
    }
  }, [cursorTime, points]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// ChargeMap is a single-pin variant for charge sessions.
//
// fixAgeSeconds is the maximum observed age of the GNSS fix across
// the charge's samples (i.e. max(sample.At - sample.LocationFixAt)).
// When supplied and large enough, we paint a small "GPS stale fix"
// badge so the user knows the marker may not reflect the actual
// charging location. Caller computes this so we don't have to thread
// raw samples through.
export function ChargeMap({
  lat,
  lon,
  height = 240,
  fixAgeSeconds,
}: {
  lat: number;
  lon: number;
  height?: number;
  fixAgeSeconds?: number | null;
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

    // GPS staleness badge. Threshold of 5 min separates "normal
    // poll cadence jitter" (a 30-60s lag is typical and not worth
    // alarming about) from "the modem stopped emitting fresh fixes"
    // — the failure mode that produces the Big Bend / Fort
    // Stockton phantom-coords class of bug.
    const STALE_THRESHOLD_S = 5 * 60;
    let staleCtl: L.Control | null = null;
    if (
      typeof fixAgeSeconds === "number" &&
      Number.isFinite(fixAgeSeconds) &&
      fixAgeSeconds >= STALE_THRESHOLD_S
    ) {
      staleCtl = stalenessBadge(map, fixAgeSeconds);
    }

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelAnimationFrame(rAF);
      ro.disconnect();
      if (staleCtl) staleCtl.remove();
      map.remove();
    };
  }, [lat, lon, fixAgeSeconds]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// ChargeBucket categorizes a session for the overview map. Mirrors the
// server-side cluster labels (Home/Public/Fast) but is computed from
// max kW alone so callers don't have to plumb the cluster API through.
//   - "fast": ≥ 30 kW peak (DCFC)
//   - "l2":   3–30 kW peak (240 V AC, public or home)
//   - "l1":   < 3 kW peak (110 V trickle / unknown)
function chargeBucket(maxKW: number): "fast" | "l2" | "l1" {
  if (!Number.isFinite(maxKW) || maxKW < 3) return "l1";
  if (maxKW >= 30) return "fast";
  return "l2";
}

const CHARGE_BUCKET_COLOR: Record<"fast" | "l2" | "l1", string> = {
  fast: "#f43f5e",
  l2: "#06b6d4",
  l1: "#a3a3a3",
};

// dotIcon is a smaller no-ring marker for overview maps where many
// pins sit in close proximity (e.g. dozens of home charges within
// 10 m). Larger ringed icons collapse into a single ambiguous blob.
function dotIcon(color: string): L.DivIcon {
  return L.divIcon({
    className: "rivolt-map-dot",
    html: `<span style="display:block;width:10px;height:10px;border-radius:9999px;background:${color};border:1.5px solid #0a0a0a;box-shadow:0 0 0 1px ${color}66;"></span>`,
    iconSize: [10, 10],
    iconAnchor: [5, 5],
  });
}

// ChargesOverviewMap renders one pin per charge, colored by power
// bucket, with a tooltip summarizing the session. Clicking a pin
// invokes onSelect (typically used to navigate to the detail page).
// Pins at the same physical spot (home charging) stack — Leaflet's
// canvas renderer handles the overlap fine; we don't cluster.
export function ChargesOverviewMap({
  charges,
  onSelect,
  height = 360,
}: {
  charges: {
    ID: string;
    Lat: number;
    Lon: number;
    StartedAt: string;
    EnergyAddedKWh: number;
    MaxPowerKW: number;
  }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  // Stash the latest onSelect so click handlers don't go stale across
  // renders (the map only mounts once per visible-charge set).
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // Build a stable signature so we only rebuild the map when the
  // visible set actually changes (not on every parent re-render).
  const sig = charges
    .map((c) => `${c.ID}:${c.Lat.toFixed(5)},${c.Lon.toFixed(5)}`)
    .join("|");

  useEffect(() => {
    if (!ref.current) return;
    const valid = charges.filter(
      (c) =>
        Number.isFinite(c.Lat) &&
        Number.isFinite(c.Lon) &&
        (c.Lat !== 0 || c.Lon !== 0),
    );
    if (valid.length === 0) return;

    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([valid[0].Lat, valid[0].Lon], 4);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addCartoDark(map);

    for (const c of valid) {
      const bucket = chargeBucket(c.MaxPowerKW);
      const m = L.marker([c.Lat, c.Lon], {
        icon: dotIcon(CHARGE_BUCKET_COLOR[bucket]),
      }).addTo(map);
      const when = new Date(c.StartedAt).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      });
      m.bindTooltip(
        `<div style="font:11px/1.3 ui-sans-serif,system-ui">` +
          `<div style="color:${CHARGE_BUCKET_COLOR[bucket]};font-weight:600">` +
          `${bucket === "fast" ? "DCFC" : bucket === "l2" ? "L2" : "L1"} · ${c.MaxPowerKW.toFixed(1)} kW</div>` +
          `<div style="color:#a3a3a3">${when}</div>` +
          `<div style="color:#d4d4d4">${c.EnergyAddedKWh.toFixed(1)} kWh added</div>` +
          `</div>`,
        { direction: "top" },
      );
      m.on("click", () => onSelectRef.current?.(c.ID));
    }
    map.fitBounds(
      L.latLngBounds(valid.map((c) => [c.Lat, c.Lon] as [number, number])),
      { padding: [24, 24], maxZoom: 14 },
    );

    const legend = new L.Control({ position: "bottomleft" });
    legend.onAdd = () => {
      const div = L.DomUtil.create("div", "rivolt-charge-legend");
      div.style.cssText =
        "background:rgba(10,10,10,0.78);border:1px solid #262626;" +
        "border-radius:6px;padding:4px 6px;display:flex;gap:8px;" +
        "align-items:center;font:10px/1.1 ui-sans-serif,system-ui;" +
        "color:#d4d4d4;backdrop-filter:blur(2px);";
      div.innerHTML = (
        [
          ["fast", "DCFC"],
          ["l2", "L2"],
          ["l1", "L1"],
        ] as const
      )
        .map(
          ([k, label]) =>
            `<span style="display:inline-flex;align-items:center;gap:3px">` +
            `<span style="display:inline-block;width:8px;height:8px;border-radius:9999px;background:${CHARGE_BUCKET_COLOR[k]}"></span>` +
            `${label}</span>`,
        )
        .join("");
      L.DomEvent.disableClickPropagation(div);
      return div;
    };
    legend.addTo(map);

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelAnimationFrame(rAF);
      ro.disconnect();
      legend.remove();
      map.remove();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}

// DrivesOverviewMap renders every drive as a thin great-circle-ish
// straight line between its start and end fix, with small dots at
// each endpoint. We deliberately don't fetch per-drive samples — the
// dataset has thousands of drives and the round-trip cost dwarfs the
// fidelity benefit at fleet scale. Click a line or endpoint to open
// the drive detail page (where the full road-snapped trace lives).
export function DrivesOverviewMap({
  drives,
  onSelect,
  height = 360,
}: {
  drives: {
    ID: string;
    StartLat: number;
    StartLon: number;
    EndLat: number;
    EndLon: number;
    StartedAt: string;
    DistanceMi: number;
  }[];
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const sig = drives.map((d) => d.ID).join("|");

  useEffect(() => {
    if (!ref.current) return;
    const valid = drives.filter(
      (d) =>
        Number.isFinite(d.StartLat) &&
        Number.isFinite(d.StartLon) &&
        Number.isFinite(d.EndLat) &&
        Number.isFinite(d.EndLon) &&
        (d.StartLat !== 0 || d.StartLon !== 0) &&
        (d.EndLat !== 0 || d.EndLon !== 0),
    );
    if (valid.length === 0) return;

    const map = L.map(ref.current, {
      zoomControl: true,
      preferCanvas: true,
      scrollWheelZoom: false,
      zoomSnap: 0.25,
      zoomDelta: 0.5,
      wheelPxPerZoomLevel: 120,
      fadeAnimation: true,
    }).setView([valid[0].StartLat, valid[0].StartLon], 4);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addCartoDark(map);

    const allLatLngs: [number, number][] = [];
    for (const d of valid) {
      const start: [number, number] = [d.StartLat, d.StartLon];
      const end: [number, number] = [d.EndLat, d.EndLon];
      allLatLngs.push(start, end);
      const when = new Date(d.StartedAt).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
      });
      const tooltip =
        `<div style="font:11px/1.3 ui-sans-serif,system-ui">` +
        `<div style="color:#34d399;font-weight:600">${d.DistanceMi.toFixed(1)} mi</div>` +
        `<div style="color:#a3a3a3">${when}</div>` +
        `</div>`;
      const line = L.polyline([start, end], {
        color: "#34d399",
        weight: 1.5,
        opacity: 0.5,
      })
        .addTo(map)
        .bindTooltip(tooltip, { sticky: true });
      line.on("click", () => onSelectRef.current?.(d.ID));
      // Endpoint dots so heavy origins/destinations (home, work) read
      // as bright clusters rather than tangles of line ends.
      L.marker(start, { icon: dotIcon("#10b981") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));
      L.marker(end, { icon: dotIcon("#f43f5e") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));
    }
    map.fitBounds(L.latLngBounds(allLatLngs), {
      padding: [24, 24],
      maxZoom: 14,
    });

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelAnimationFrame(rAF);
      ro.disconnect();
      map.remove();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig]);

  return (
    <div
      ref={ref}
      className="rounded-lg overflow-hidden border border-neutral-800"
      style={{ height }}
    />
  );
}
