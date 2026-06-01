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
import {
  valhallaBase,
  ensureConfig,
  tilesPMTilesURL,
} from "../lib/config";
import { leafletLayer, paintRules, labelRules } from "protomaps-leaflet";
import { namedFlavor, type Flavor as PMFlavor } from "@protomaps/basemaps";
import { findNearestCharger, type POI } from "../lib/poi";
import {
  cleanTrace,
  classifySegment,
  splitTraceOnGaps,
  haversine,
  type TraceGap,
} from "../lib/trace";

// SnapPoint is the per-sample shape consumed by the snap pipeline.
// Caller fills in {lat, lon}; t (unix seconds) and s (speed mph) are
// optional and only used to help the matcher / colour the polyline.
type SnapPoint = { lat: number; lon: number; t?: number; s?: number };

// valhallaRoutePair returns a road-routed polyline between two coords
// or null on failure. Used to fill gaps where map-matching gave up
// (between sub-matchings, or across true GPS dropouts) with a path
// that follows actual roads instead of a straight chord.
async function valhallaRoutePair(
  from: [number, number],
  to: [number, number],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "") return null;
  const body = {
    locations: [
      { lat: from[0], lon: from[1], type: "break" },
      { lat: to[0], lon: to[1], type: "break" },
    ],
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
      const seg = decodePolyline(legs[i].shape, 6);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// valhallaSnap calls /trace_route with walk_or_snap in OSRM-compatible
// response format. Returns an ordered list of snapped sub-segments
// plus dashed gap connectors for the input ranges Valhalla couldn't
// match continuously. Walk_or_snap routinely splits a noisy trace
// into 2+ `matchings` — the previous Valhalla-native response shape
// surfaced only the longest match in `trip` and buried the rest in
// `alternates`, which we'd silently drop. OSRM format gives us the
// `tracepoints` array (one entry per input shape point) mapping each
// input point to its matching index, which is how we know where the
// matcher gave up.
async function valhallaSnap(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<{ chunks: [number, number][][]; gaps: TraceGap[] } | null> {
  const base = valhallaBase();
  if (base === "" || pts.length < 2) return null;
  const MAX_TRACE = 1500;
  let trace = pts;
  if (trace.length > MAX_TRACE) {
    const step = Math.ceil(trace.length / MAX_TRACE);
    trace = trace.filter((_, i) => i % step === 0);
    if (trace[trace.length - 1] !== pts[pts.length - 1]) {
      trace.push(pts[pts.length - 1]);
    }
  }
  const body = {
    shape: trace.map((p) =>
      Number.isFinite(p.t)
        ? { lat: p.lat, lon: p.lon, time: p.t as number }
        : { lat: p.lat, lon: p.lon },
    ),
    costing: "auto",
    shape_match: "walk_or_snap",
    trace_options: { search_radius: 100, gps_accuracy: 25 },
    format: "osrm",
  };
  try {
    const r = await fetch(`${base}/trace_route`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!r.ok) return null;
    const j = (await r.json()) as {
      code?: string;
      matchings?: { geometry?: string; confidence?: number }[];
      tracepoints?: ({ matchings_index: number } | null)[];
    };
    const matchings = j.matchings ?? [];
    const tps = j.tracepoints ?? [];
    if (matchings.length === 0) return null;
    // OSRM polylines from Valhalla are precision-6.
    const chunks: [number, number][][] = matchings.map((m) =>
      m.geometry ? decodePolyline(m.geometry, 6) : [],
    );
    // Walk the tracepoints to find each matching's input-span boundaries.
    // The last input point of matching N and the first input point of
    // matching N+1 frame the unmatched stretch we render as a dashed
    // connector. We use the input coords (not the snapped endpoints)
    // because the gap really lives in input-space — Valhalla doesn't
    // emit a polyline for it.
    const lastInputOf: number[] = new Array(matchings.length).fill(-1);
    const firstInputOf: number[] = new Array(matchings.length).fill(-1);
    for (let i = 0; i < tps.length; i++) {
      const tp = tps[i];
      if (!tp) continue;
      const k = tp.matchings_index;
      if (k < 0 || k >= matchings.length) continue;
      if (firstInputOf[k] === -1) firstInputOf[k] = i;
      lastInputOf[k] = i;
    }
    const gaps: TraceGap[] = [];
    for (let k = 1; k < matchings.length; k++) {
      const prevEnd = lastInputOf[k - 1];
      const curStart = firstInputOf[k];
      if (prevEnd < 0 || curStart < 0) continue;
      const a = trace[prevEnd];
      const b = trace[curStart];
      gaps.push({ from: [a.lat, a.lon], to: [b.lat, b.lon] });
    }
    const valid = chunks.filter((c) => c.length > 1);
    return valid.length > 0 ? { chunks: valid, gaps } : null;
  } catch {
    return null;
  }
}

type SnapPlan = {
  segments: { coords: [number, number][]; raw: boolean }[];
  // Gaps the renderer couldn't fill with a routed polyline — drawn
  // as dashed neutral connectors so the user can tell the matcher
  // gave up here.
  gaps: TraceGap[];
  // Routed gap-fills: where the matcher gave up between sub-matches
  // (or between split segments) but Valhalla's /route engine could
  // produce a road-following polyline between the endpoints. Drawn
  // as solid neutral lines so the eye reads them as "inferred path"
  // rather than recorded telemetry.
  routedGaps: [number, number][][];
};

// Render the raw GPS chord polyline for a segment we don't want to
// trust the matcher with — parking-lot maneuvers, sub-200m drives,
// trace-too-short cases. The raw trace IS the actual recorded path,
// snapping only adds hallucination.
function rawPolyline(seg: SnapPoint[]): [number, number][] {
  return seg.map((p) => [p.lat, p.lon]);
}

async function snapToRoads(
  rawPoints: SnapPoint[],
  signal: AbortSignal,
): Promise<SnapPlan | null> {
  const points = cleanTrace(rawPoints);
  // Fallback to the unfiltered raw points when cleaning leaves too
  // little to work with — a sparsely-sampled drive should still
  // render a polyline rather than nothing at all.
  if (points.length < 2) {
    const raw = rawPoints.filter(
      (p) => Number.isFinite(p.lat) && Number.isFinite(p.lon),
    );
    if (raw.length < 2) return null;
    return {
      segments: [{ coords: rawPolyline(raw), raw: true }],
      gaps: [],
      routedGaps: [],
    };
  }
  await ensureConfig();
  if (signal.aborted) return null;

  const { segments, gaps } = splitTraceOnGaps(points);
  if (segments.length === 0) {
    return {
      segments: [{ coords: rawPolyline(points), raw: true }],
      gaps: [],
      routedGaps: [],
    };
  }

  const out: SnapPlan = { segments: [], gaps, routedGaps: [] };
  const valhalla = valhallaBase() !== "";

  for (const seg of segments) {
    if (signal.aborted) return null;
    const quality = classifySegment(seg);
    if (quality === "trivial") {
      out.segments.push({ coords: rawPolyline(seg), raw: true });
      continue;
    }
    let chunks: [number, number][][] | null = null;
    let innerGaps: TraceGap[] = [];
    if (valhalla) {
      const v = await valhallaSnap(seg, signal);
      if (v) {
        chunks = v.chunks;
        innerGaps = v.gaps;
      }
    }
    if (signal.aborted) return null;
    if (chunks && chunks.length > 0) {
      // Push each snapped sub-segment as its own out.segment so the
      // renderer can draw dashed connectors between them. innerGaps
      // (Valhalla telling us where it gave up matching) bubble up to
      // out.gaps alongside the trace-level gaps from splitTraceOnGaps.
      for (const c of chunks) out.segments.push({ coords: c, raw: false });
      out.gaps.push(...innerGaps);
      // Head/tail coverage: the first matching may not start at the
      // segment's first input point, and the last may not end at the
      // last input point. Wrap with raw chords so the full recorded
      // trace remains visible end-to-end.
      const TAIL_TOLERANCE_M = 200;
      const firstSnap = chunks[0][0];
      const headDist = haversine(
        { lat: seg[0].lat, lon: seg[0].lon },
        { lat: firstSnap[0], lon: firstSnap[1] },
      );
      if (headDist > TAIL_TOLERANCE_M) {
        out.gaps.push({
          from: [seg[0].lat, seg[0].lon],
          to: firstSnap,
        });
      }
      const lastSnap = chunks[chunks.length - 1][chunks[chunks.length - 1].length - 1];
      const tailPt = seg[seg.length - 1];
      const tailDist = haversine(
        { lat: lastSnap[0], lon: lastSnap[1] },
        { lat: tailPt.lat, lon: tailPt.lon },
      );
      if (tailDist > TAIL_TOLERANCE_M) {
        out.gaps.push({
          from: lastSnap,
          to: [tailPt.lat, tailPt.lon],
        });
      }
    } else {
      // Snap failure: render the raw chord — better than nothing.
      out.segments.push({ coords: rawPolyline(seg), raw: true });
    }
  }

  // Route-fill the gaps. For each unfilled gap, ask Valhalla's /route
  // engine for a road-following polyline between the endpoints. On
  // success the gap becomes a routedGaps polyline (rendered solid +
  // neutral so it reads as "inferred path") and the dashed connector
  // is dropped. On failure we keep the dashed gap so the user can
  // still tell the matcher gave up here. Issued in parallel since
  // each /route call is independent.
  if (valhalla && out.gaps.length > 0) {
    const filled = await Promise.all(
      out.gaps.map((g) => valhallaRoutePair(g.from, g.to, signal)),
    );
    if (signal.aborted) return null;
    const remaining: TraceGap[] = [];
    for (let i = 0; i < out.gaps.length; i++) {
      const routed = filled[i];
      if (routed && routed.length > 1) {
        out.routedGaps.push(routed);
      } else {
        remaining.push(out.gaps[i]);
      }
    }
    out.gaps = remaining;
  }

  return out.segments.length > 0 ? out : null;
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

// Vector attribution for the self-hosted Protomaps basemap. OSM is
// the data source; Protomaps builds and hosts the daily planet
// extracts we slice from at build time.
const PROTOMAPS_ATTRIB =
  '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> · <a href="https://protomaps.com">Protomaps</a>';

// Minimal HTML-entity escape for the few places we inject untrusted
// strings (OSM POI names) into innerHTML / popup content. Just the
// five XML-spec essentials; we never inject inside script or URL
// contexts so this is sufficient.
function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    c === "&"
      ? "&amp;"
      : c === "<"
      ? "&lt;"
      : c === ">"
      ? "&gt;"
      : c === '"'
      ? "&quot;"
      : "&#39;",
  );
}

// Friendly labels for the OSM `socket:*` keys we care about. Keys
// not in this map fall through to a tidied-up version of the raw
// tag (underscores → spaces, title-cased).
const SOCKET_LABELS: Record<string, string> = {
  type1: "J1772",
  type1_combo: "CCS1",
  type2: "Type 2",
  type2_combo: "CCS2",
  type2_cable: "Type 2 (cable)",
  chademo: "CHAdeMO",
  tesla_supercharger: "Supercharger",
  tesla_supercharger_ccs: "Supercharger (CCS)",
  tesla_destination: "Tesla Destination",
  nacs: "NACS",
};

function prettySocket(s: string): string {
  if (SOCKET_LABELS[s]) return SOCKET_LABELS[s];
  return s
    .split("_")
    .map((w) => (w ? w[0].toUpperCase() + w.slice(1) : w))
    .join(" ");
}

// Build the OSM "view this feature" link. Prefers the canonical
// /node/<id> URL when tippecanoe preserved the OSM id; falls back
// to the lat/lon-anchored marker URL otherwise.
function osmLinkURL(poi: POI): string {
  if (poi.osmId && /^(node|way|relation)\/\d+$/.test(poi.osmId)) {
    return `https://www.openstreetmap.org/${poi.osmId}`;
  }
  return `https://www.openstreetmap.org/?mlat=${poi.lat}&mlon=${poi.lon}#map=19/${poi.lat}/${poi.lon}`;
}

// Render the charger spec list as inner HTML for the popup. Only
// emits rows for fields that are present, so a sparsely-tagged
// charger doesn't show empty placeholders. Returns "" when nothing
// is known (basemap fallback path) — the popup falls back to just
// the name + snap-distance footer.
function chargerSpecListHTML(poi: POI): string {
  if (poi.source !== "chargers") {
    // Basemap fallback strips POI tags down to name only. Surface
    // a "limited data" hint so the user knows we matched something,
    // it just doesn't carry network/power/socket metadata.
    return (
      '<div style="color:#a3a3a3;font-size:11px;line-height:1.5;font-style:italic">' +
      "Limited data — basemap snap (no network or power info)" +
      "</div>"
    );
  }
  const rows: string[] = [];
  const row = (label: string, value: string) =>
    rows.push(
      `<div style="display:flex;justify-content:space-between;gap:8px;font-size:11px;line-height:1.5">` +
        `<span style="color:#a3a3a3">${escapeHTML(label)}</span>` +
        `<span style="color:#fafafa;text-align:right">${value}</span>` +
        `</div>`,
    );
  // Network beats operator when both are present — most users think
  // of "EVgo" or "Electrify America" first, "operated by ..." second.
  const network = poi.network ?? poi.brand;
  if (network) row("Network", escapeHTML(network));
  if (poi.operator && poi.operator !== network) {
    row("Operator", escapeHTML(poi.operator));
  }
  if (poi.maxPowerKW != null && poi.maxPowerKW > 0) {
    // Format as integer kW for ≥1 kW values; trailing .x rarely
    // matters and clutters the popup.
    const kw = poi.maxPowerKW >= 10
      ? Math.round(poi.maxPowerKW).toString()
      : poi.maxPowerKW.toFixed(1);
    row("Max power", `${kw} kW`);
  }
  if (poi.capacity && poi.capacity > 0) {
    row("Stalls", String(poi.capacity));
  }
  if (poi.socketTypes && poi.socketTypes.length > 0) {
    const labels = poi.socketTypes.map(prettySocket).join(", ");
    row("Connectors", escapeHTML(labels));
    // Tesla Superchargers expose the NACS plug. Rivians need a
    // NACS↔CCS1 adapter to use them, unless the station also
    // advertises CCS1 (Magic Dock retrofits, type1_combo) — in
    // which case there's a native CCS1 cable on-site and no
    // adapter is needed. Same logic the trip planner uses for
    // its "Tesla adapter required" hint.
    const hasTesla = poi.socketTypes.includes("tesla_supercharger");
    const hasCCS1 = poi.socketTypes.includes("type1_combo");
    if (hasTesla && !hasCCS1) {
      rows.push(
        `<div style="font-size:11px;line-height:1.5;color:#fbbf24;margin-top:2px">` +
          "Tesla adapter required" +
          `</div>`,
      );
    }
  }
  if (poi.fee) {
    const v = poi.fee.toLowerCase();
    const friendly =
      v === "yes" ? "Paid" : v === "no" ? "Free" : escapeHTML(poi.fee);
    row("Fee", friendly);
  }
  if (poi.openingHours) {
    row("Hours", escapeHTML(poi.openingHours));
  }
  if (rows.length === 0) return "";
  return (
    `<div style="margin:6px 0;padding:6px 0;border-top:1px solid #262626;border-bottom:1px solid #262626">` +
    rows.join("") +
    `</div>`
  );
}

// Basemap flavor toggle. protomaps-leaflet ships four named flavors
// Basemap flavor toggle. @protomaps/basemaps ships four named
// flavors (dark / light / black / white) but black is a marginal
// variant of dark and white is a marginal variant of light --
// not enough difference to justify four buttons in the UI. We
// expose just dark and light. Persist the choice in localStorage
// so it survives reloads. A custom event keeps all maps on the
// page in sync -- switching flavor on the drive details page
// also updates the inline mini-map in the sidebar (or any other
// map mounted concurrently).
type Flavor = "dark" | "light";
const FLAVORS: Flavor[] = ["dark", "light"];
const FLAVOR_LS_KEY = "rivolt:basemap-flavor";
const FLAVOR_EVENT = "rivolt:flavor-change";

function getFlavor(): Flavor {
  try {
    const v = localStorage.getItem(FLAVOR_LS_KEY);
    if (v && (FLAVORS as string[]).includes(v)) return v as Flavor;
  } catch {
    // localStorage can throw in private mode / disabled storage;
    // fall through to the default.
  }
  return "dark";
}

function setFlavor(f: Flavor): void {
  try {
    localStorage.setItem(FLAVOR_LS_KEY, f);
  } catch {
    // see getFlavor — non-fatal.
  }
  window.dispatchEvent(new CustomEvent<Flavor>(FLAVOR_EVENT, { detail: f }));
}

// flavorControl renders a two-button picker in the top-right
// corner for swapping between dark and light basemap flavors.
// Click dispatches the FLAVOR_EVENT so every mounted map swaps
// its layer in unison; the active button is highlighted on the
// next event dispatch (initial paint and subsequent changes
// alike).
function flavorControl(map: L.Map): L.Control {
  const ctl = new L.Control({ position: "topright" });
  ctl.onAdd = () => {
    const div = L.DomUtil.create("div", "rivolt-flavor-control");
    div.style.cssText =
      "background:rgba(10,10,10,0.78);border:1px solid #262626;" +
      "border-radius:6px;padding:2px;display:inline-flex;gap:2px;" +
      "font:10px/1 ui-sans-serif,system-ui;backdrop-filter:blur(2px);" +
      "box-shadow:0 1px 2px rgba(0,0,0,0.4);";
    const buttons: Record<Flavor, HTMLButtonElement> = {} as Record<
      Flavor,
      HTMLButtonElement
    >;
    for (const f of FLAVORS) {
      const btn = L.DomUtil.create("button", "", div) as HTMLButtonElement;
      btn.type = "button";
      btn.textContent = f[0].toUpperCase();
      btn.title = `${f[0].toUpperCase()}${f.slice(1)} basemap`;
      btn.style.cssText =
        "width:20px;height:20px;border:1px solid transparent;" +
        "border-radius:4px;background:transparent;color:#a3a3a3;" +
        "cursor:pointer;padding:0;font:inherit;font-weight:600;";
      L.DomEvent.on(btn, "click", (e) => {
        L.DomEvent.stop(e);
        setFlavor(f);
      });
      buttons[f] = btn;
    }
    const refresh = (active: Flavor) => {
      for (const f of FLAVORS) {
        const b = buttons[f];
        if (f === active) {
          b.style.background = "#262626";
          b.style.color = "#fafafa";
          b.style.borderColor = "#404040";
        } else {
          b.style.background = "transparent";
          b.style.color = "#a3a3a3";
          b.style.borderColor = "transparent";
        }
      }
    };
    refresh(getFlavor());
    const onChange = (e: Event) => {
      refresh((e as CustomEvent<Flavor>).detail);
    };
    window.addEventListener(FLAVOR_EVENT, onChange);
    L.DomEvent.disableClickPropagation(div);
    L.DomEvent.disableScrollPropagation(div);
    map.once("unload", () => {
      window.removeEventListener(FLAVOR_EVENT, onChange);
    });
    return div;
  };
  ctl.addTo(map);
  return ctl;
}

// boostContrast returns a copy of a named protomaps flavor with
// building / POI / landuse colors lifted so they actually read
// against the earth color at street zoom. The shipped named
// flavors are tuned for "minimal" cartography -- DARK puts
// buildings at #111 over an earth of #1f1f1f at 50% opacity,
// which composites to roughly the same gray and erases building
// footprints visually. Same story in LIGHT (buildings = #ccc on
// earth = #e2dfda) and BLACK / WHITE.
//
// We only override fields where the shipped value visually
// merges with earth; other fields (roads, water, labels) are
// left untouched so the overall theme still feels right.
function boostContrast(name: Flavor, base: PMFlavor): PMFlavor {
  switch (name) {
    case "dark":
      return {
        ...base,
        // Buildings need to be clearly above earth (#1f1f1f).
        // #555 at 0.5 opacity composites to ~#3a -- visible but
        // not loud.
        buildings: "#555555",
        // Parks / wood / pedestrian areas: lift the green tint
        // so a city park reads as "this is a park," not "this
        // is slightly different gray."
        park_a: "#1d3527",
        park_b: "#1d3527",
        wood_a: "#1d2d24",
        wood_b: "#1d2d24",
        pedestrian: "#2a2a2a",
        // Industrial / commercial / school landuse: subtle
        // warm-gray so big-box parking lots stop looking like
        // empty earth.
        industrial: "#2c2a26",
        school: "#2c2828",
        hospital: "#2c2828",
      };
    case "light":
      return {
        ...base,
        // LIGHT ships buildings = #cccccc over earth = #e2dfda
        // at 0.5 opacity -- fully invisible. Drop to a clearly
        // darker tone.
        buildings: "#a8a8a8",
        park_a: "#cfe5cf",
        park_b: "#cfe5cf",
        pedestrian: "#dad7d2",
      };
  }
}

// addBasemap picks between the self-hosted PMTiles vector layer
// (when the tile server is wired via /api/config) and the legacy
// CARTO raster split-label setup. The vector path renders labels
// inline with the basemap; the route polyline draws on top.
//
// When the vector path is active, the layer is swapped on
// FLAVOR_EVENT so every map on the page stays in sync with the
// flavor picker. Listener is removed on map unload.
function addBasemap(map: L.Map) {
  const url = tilesPMTilesURL();
  if (!url) {
    addCartoDark(map);
    return;
  }
  // protomaps-leaflet's leafletLayer returns its own LeafletLayer
  // class which extends L.GridLayer at runtime but isn't typed as
  // a Leaflet Layer in the published .d.ts. Cast at the boundary.
  //
  // We don't pass `flavor: f` directly: protomaps-leaflet's named
  // flavors (especially `dark` and `light`) ship with building /
  // POI / landuse colors so close to the earth color that
  // buildings effectively disappear at street zoom. We compute
  // paintRules/labelRules ourselves from a contrast-boosted copy
  // of the named flavor so building footprints, parks, and
  // service-area landuse all read clearly at z16+.
  const mkLayer = (f: Flavor) => {
    const base = namedFlavor(f);
    const boosted = boostContrast(f, base);
    return leafletLayer({
      url,
      paintRules: paintRules(boosted),
      labelRules: labelRules(boosted, "en"),
      backgroundColor: boosted.background,
      attribution: PROTOMAPS_ATTRIB,
    }) as unknown as L.Layer;
  };
  let current = mkLayer(getFlavor());
  current.addTo(map);
  const onChange = (e: Event) => {
    const next = (e as CustomEvent<Flavor>).detail;
    const replacement = mkLayer(next);
    replacement.addTo(map);
    map.removeLayer(current);
    current = replacement;
  };
  window.addEventListener(FLAVOR_EVENT, onChange);
  map.once("unload", () => {
    window.removeEventListener(FLAVOR_EVENT, onChange);
  });
  flavorControl(map);
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
// drawRoute composes the polyline layers into a LayerGroup and
// returns it without attaching to the map — the caller decides where
// it goes (top-level for single-segment, nested under a parent group
// for multi-segment trips).
function drawRoute(
  _map: L.Map,
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
    // The most recent drive often has no end telemetry yet, so the
    // caller's start/end fallback (drive.Start/EndLat) can be NaN. An
    // unvalidated NaN reaches L.marker / latLngBounds and throws,
    // killing the whole map render.
    const finite = (p?: Point): Point | undefined =>
      p && Number.isFinite(p.lat) && Number.isFinite(p.lon) ? p : undefined;
    const startPt = finite(start);
    const endPt = finite(end);
    const fallback: Point | undefined = startPt ?? endPt ?? valid[0];
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

    addBasemap(map);

    // Pick start/end markers. Prefer caller-supplied start/end (the
    // page can derive these from parked samples flanking the drive,
    // which is more accurate than any in-drive GPS fix because
    // telemetry frequently misses the first minute of a trip).
    // Fall back to the polyline endpoints when no hint is given.
    const lineStart: Point | undefined = startPt ?? valid[0];
    const lineEnd: Point | undefined = endPt ?? valid[valid.length - 1];

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
    if (startPt) {
      latlngs.push([startPt.lat, startPt.lon]);
      speeds.push(0); // parked
    }
    for (const p of valid) {
      latlngs.push([p.lat, p.lon]);
      speeds.push(p.s);
    }
    if (endPt && !sameSpot) {
      latlngs.push([endPt.lat, endPt.lon]);
      speeds.push(0); // parked
    }
    let line: L.LayerGroup | null = null;
    if (latlngs.length > 1) {
      line = drawRoute(map, latlngs, speeds);
      line.addTo(map);
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

    // Best-effort: replace the straight-line polyline with a
    // road-snapped geometry from Valhalla. If the request fails
    // (offline, non-drivable terrain) we keep the raw trace. The
    // abort controller cancels the in-flight request if the
    // component unmounts or props change before Valhalla responds.
    const ac = new AbortController();
    const valhallaAvailable = valhallaBase() !== "";
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
      ...(startPt ? [{ ...startPt, t: startT }] : []),
      ...valid,
      ...(endPt && !sameSpot ? [{ ...endPt, t: endT }] : []),
    ];
    if (valhallaAvailable) {
      snapToRoads(tracePoints, ac.signal).then((plan) => {
        if (!plan || plan.segments.length === 0 || !mapRef.current) return;
        if (line) line.remove();
        const layers = L.layerGroup();
        const allCoords: [number, number][] = [];
        for (const { coords: matched } of plan.segments) {
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
          for (const inner of drawRoute(map, matched, matchedSpeeds).getLayers()) {
            layers.addLayer(inner);
          }
          allCoords.push(...matched);
        }
        // Routed gap-fills: Valhalla's /route engine produced a
        // road-following polyline across an unmatched stretch. Solid
        // neutral colour so the eye distinguishes inferred path from
        // GPS-recorded (speed-coloured) telemetry.
        for (const routed of plan.routedGaps) {
          L.polyline(routed, {
            color: "#737373",
            weight: 2.5,
            opacity: 0.75,
          }).addTo(layers);
          allCoords.push(...routed);
        }
        // Dashed connectors only when /route also failed — true
        // unknown stretches where the matcher gave up and the
        // router couldn't connect the endpoints either.
        for (const gap of plan.gaps) {
          L.polyline([gap.from, gap.to], {
            color: "#737373",
            weight: 2,
            opacity: 0.7,
            dashArray: "6 6",
          }).addTo(layers);
        }
        layers.addTo(map);
        if (allCoords.length > 0) {
          map.fitBounds(L.latLngBounds(allCoords), { padding: [20, 20] });
        }
        line = layers;
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
// No GPS-staleness badge here. The badge on DriveMap warns that a
// moving polyline may be miles off from where the vehicle actually
// drove; on a parked car the recorded charge marker is fine even if
// the modem stopped emitting fresh fixes during the session — and
// parked vehicles produce stale fixes routinely (no movement → the
// modem stops urgently re-fixing). The badge fired on most charges
// with zero actionable signal, so we removed the prop entirely.
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
    }).setView([lat, lon], 16);
    map.on("click", () => map.scrollWheelZoom.enable());
    map.on("mouseout", () => map.scrollWheelZoom.disable());
    addBasemap(map);

    // Layered marker model:
    //   - Always show the recorded GPS position (where the vehicle
    //     actually pinged from). Starts as the prominent amber pin.
    //   - Async query the basemap pmtiles for the nearest OSM
    //     charging_station POI within 250 m. If found, demote the
    //     recorded-GPS pin to a small muted dot, draw a dashed
    //     connector, and paint a labeled charger pin at the OSM
    //     coords. The label is the OSM `name` tag.
    //
    // We can't determine "which physical charger" deterministically
    // from a single GPS sample (parking lots routinely host several
    // stations within 50 m of each other), so we pick the closest
    // and show the user the snap so they can sanity-check it.
    const recordedMarker = L.marker([lat, lon], {
      icon: circleIcon("#f59e0b"),
      zIndexOffset: 1000,
    })
      .addTo(map)
      .bindTooltip("Charge location", { direction: "top" });

    let chargerMarker: L.Marker | null = null;
    let connector: L.Polyline | null = null;
    let cancelled = false;

    // Attempt the snap unconditionally — findNearestCharger awaits
    // /api/config internally and exits cleanly if neither archive is
    // wired. 750m radius covers the typical "GPS landed at
    // parking-lot entrance, OSM marker is in the middle of the lot"
    // geometry; 250m was too tight for big-box and travel-plaza
    // stations.
    void findNearestCharger(lat, lon, 750).then((poi) => {
      if (cancelled || !poi) return;
      applyChargerSnap(poi);
    });

    function applyChargerSnap(poi: POI) {
      // Demote recorded-GPS to a muted dot with a clarifying
      // tooltip. Keeping it visible (rather than removing it)
      // surfaces the snap distance for the user.
      recordedMarker.setIcon(
        L.divIcon({
          className: "rivolt-map-recorded-gps",
          html:
            '<span style="display:block;width:8px;height:8px;border-radius:9999px;' +
            'background:#737373;border:1.5px solid #0a0a0a;opacity:0.85;"></span>',
          iconSize: [8, 8],
          iconAnchor: [4, 4],
        }),
      );
      recordedMarker.setZIndexOffset(500);
      recordedMarker.unbindTooltip();
      recordedMarker.bindTooltip(
        `Recorded GPS · ${Math.round(poi.distanceM)} m from charger`,
        { direction: "top" },
      );

      // Dashed connector from recorded GPS to the snapped charger
      // so the user immediately sees the relationship between the
      // two pins.
      connector = L.polyline(
        [
          [lat, lon],
          [poi.lat, poi.lon],
        ],
        {
          color: "#f59e0b",
          weight: 1.5,
          opacity: 0.6,
          dashArray: "4 4",
          interactive: false,
        },
      ).addTo(map);

      // The snapped charger pin: amber bolt-styled square with the
      // OSM name as a permanent label so the charger identifies
      // itself without requiring a click.
      const labelText = poi.name ?? "Charging station";
      chargerMarker = L.marker([poi.lat, poi.lon], {
        icon: L.divIcon({
          className: "rivolt-charger-pin",
          html:
            '<span style="display:inline-flex;align-items:center;justify-content:center;' +
            "width:22px;height:22px;border-radius:6px;background:#f59e0b;" +
            'border:2px solid #0a0a0a;box-shadow:0 0 0 2px #f59e0b33;' +
            'color:#0a0a0a;font:700 13px/1 ui-sans-serif,system-ui;">⚡</span>',
          iconSize: [22, 22],
          iconAnchor: [11, 11],
        }),
        zIndexOffset: 2000,
      })
        .addTo(map)
        .bindTooltip(escapeHTML(labelText), {
          direction: "top",
          permanent: true,
          offset: [0, -10],
          className: "rivolt-charger-label",
        })
        .bindPopup(
          '<div style="font:12px/1.4 ui-sans-serif,system-ui;color:#fafafa;min-width:180px;max-width:260px">' +
            `<div style="font-weight:600;margin-bottom:2px">${escapeHTML(labelText)}</div>` +
            chargerSpecListHTML(poi) +
            '<div style="color:#a3a3a3;font-size:11px;margin-top:4px">' +
            `Snapped from <a href="${osmLinkURL(poi)}" target="_blank" rel="noopener" style="color:#f59e0b;text-decoration:underline">OpenStreetMap</a> · ${Math.round(poi.distanceM)} m from recorded GPS` +
            "</div></div>",
        );

      // Re-center on the snapped charger if it falls within the
      // current viewport bounds; otherwise leave the view alone so
      // we don't pan unexpectedly far when the snap distance is
      // close to the radius limit.
      if (map.getBounds().contains([poi.lat, poi.lon])) {
        map.panTo([poi.lat, poi.lon], { animate: true });
      }
    }

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      cancelled = true;
      cancelAnimationFrame(rAF);
      ro.disconnect();
      if (chargerMarker) chargerMarker.remove();
      if (connector) connector.remove();
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
    addBasemap(map);

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

// decodePolyline decodes a Google-format encoded polyline into
// [lat, lon] pairs. Matches the encoder in
// internal/rivian/polyline.go on the backend at the default
// precision. precision defaults to 5 (Google encoded polyline);
// pass 6 for Valhalla shapes. Returns an empty array for empty / nullish /
// malformed input so callers can do a simple length check and
// fall back to a straight start→end line.
function decodePolyline(
  s: string | undefined | null,
  precision: 5 | 6 = 5,
): [number, number][] {
  if (!s) return [];
  const factor = precision === 6 ? 1e-6 : 1e-5;
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

// routeOverview fetches a road-snapped start→end geometry for drives
// that don't have a stored RoutePolyline. Lightweight: only two
// coordinates, no GPS trace required. Returns null when Valhalla
// isn't wired or the call fails, so the caller can keep the
// straight-line fallback.
async function routeOverview(
  startLat: number,
  startLon: number,
  endLat: number,
  endLon: number,
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  await ensureConfig();
  if (signal.aborted) return null;
  if (valhallaBase() === "") return null;
  const pts: SnapPoint[] = [
    { lat: startLat, lon: startLon },
    { lat: endLat, lon: endLon },
  ];
  return await valhallaRoute(pts, signal);
}

// valhallaRoute is the cheapest-path equivalent of routeAll, used by
// the multi-drive overview map. POST /route with two break points and
// decode the polyline6 shape.
async function valhallaRoute(
  pts: SnapPoint[],
  signal: AbortSignal,
): Promise<[number, number][] | null> {
  const base = valhallaBase();
  if (base === "" || pts.length < 2) return null;
  const body = {
    locations: pts.map((p) => ({ lat: p.lat, lon: p.lon })),
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
      const seg = decodePolyline(legs[i].shape, 6);
      if (i > 0 && seg.length > 0) seg.shift();
      out.push(...seg);
    }
    return out.length > 1 ? out : null;
  } catch {
    return null;
  }
}

// DrivesOverviewMap renders every drive's route on a single map.
// Drives with a stored RoutePolyline (live recorder) show the real
// GPS trace. Legacy drives (ElectraFi imports) fetch a road-snapped
// geometry from Valhalla using just the start/end coordinates.
const ROUTE_CONCURRENCY = 20;

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
    RoutePolyline?: string;
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
    addBasemap(map);

    const ac = new AbortController();
    const allLatLngs: [number, number][] = [];

    // Render initial polylines (stored trace or straight-line
    // placeholder) and collect drives that need a Valhalla /route fetch.
    const needsRoute: {
      d: (typeof valid)[0];
      line: L.Polyline;
    }[] = [];

    for (const d of valid) {
      const start: [number, number] = [d.StartLat, d.StartLon];
      const end: [number, number] = [d.EndLat, d.EndLon];
      const trace = decodePolyline(d.RoutePolyline);
      const path: [number, number][] = trace.length >= 2 ? trace : [start, end];
      for (const p of path) allLatLngs.push(p);
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
      const line = L.polyline(path, {
        color: "#34d399",
        weight: 1.5,
        opacity: 0.5,
      })
        .addTo(map)
        .bindTooltip(tooltip, { sticky: true });
      line.on("click", () => onSelectRef.current?.(d.ID));
      L.marker(start, { icon: dotIcon("#10b981") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));
      L.marker(end, { icon: dotIcon("#f43f5e") })
        .addTo(map)
        .on("click", () => onSelectRef.current?.(d.ID));

      // Queue a Valhalla /route fetch only for drives without a stored polyline.
      if (trace.length < 2) {
        needsRoute.push({ d, line });
      }
    }

    map.fitBounds(L.latLngBounds(allLatLngs), {
      padding: [24, 24],
      maxZoom: 14,
    });

    // Async pass: replace straight-line placeholders with road-snapped
    // routes, ROUTE_CONCURRENCY at a time.
    if (needsRoute.length > 0) {
      const queue = [...needsRoute];
      const worker = async () => {
        while (queue.length > 0 && !ac.signal.aborted) {
          const item = queue.shift()!;
          const routed = await routeOverview(
            item.d.StartLat,
            item.d.StartLon,
            item.d.EndLat,
            item.d.EndLon,
            ac.signal,
          );
          if (routed && routed.length >= 2 && !ac.signal.aborted) {
            item.line.setLatLngs(routed);
          }
        }
      };
      const workers = Array.from(
        { length: Math.min(ROUTE_CONCURRENCY, needsRoute.length) },
        worker,
      );
      void Promise.all(workers);
    }

    const invalidate = () => map.invalidateSize();
    const rAF = requestAnimationFrame(() => setTimeout(invalidate, 0));
    const ro = new ResizeObserver(invalidate);
    ro.observe(ref.current);
    return () => {
      ac.abort();
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
