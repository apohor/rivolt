// POI lookup against self-hosted PMTiles archives.
//
// We query two archives, in priority order:
//
//   1. /api/maps/tiles/chargers.pmtiles (when configured) — a
//      dedicated archive built from a North America Geofabrik
//      extract via osmium tags-filter + tippecanoe with the full
//      OSM tag bag preserved. This gives us operator, network,
//      brand, capacity, socket types, max kW, fee, and
//      opening_hours per charger — the things charge-detail
//      popups actually need.
//
//   2. /api/maps/tiles/us.pmtiles (the basemap archive) — used
//      as a fallback when the chargers archive isn't deployed.
//      The protomaps planet build strips POI tags down to just
//      name/kind/min_zoom, so we get a snap point but no
//      operator/network/kW info.
//
// Both archives are reached through the rivolt API's same-origin
// /api/maps/tiles proxy, so authn is automatic via the session
// cookie. PMTiles + @mapbox/vector-tile are already in the SPA
// bundle as transitive deps of protomaps-leaflet, so reading the
// raw tile bytes costs nothing extra at runtime.

import Pbf from "pbf";
import { VectorTile } from "@mapbox/vector-tile";
import { PMTiles, FetchSource } from "pmtiles";
import { tilesPMTilesURL, chargersPMTilesURL, ensureConfig } from "./config";

export type POI = {
  // Snapped coords from the OSM POI node.
  lat: number;
  lon: number;
  // Whatever's on the OSM `name` tag, or undefined when unnamed.
  name?: string;
  // OSM `kind` (always "charging_station" for findNearestCharger).
  kind: string;
  // Distance in meters from the query point.
  distanceM: number;

  // Charger-specific tags. Only populated when the result came
  // from the chargers archive (full tag bag). Undefined when the
  // basemap fallback was used.
  operator?: string;
  network?: string;
  brand?: string;
  capacity?: number;
  socketTypes?: string[];
  maxPowerKW?: number;
  fee?: string;
  openingHours?: string;
  // OSM canonical id (`node/12345`) when known. Lets the popup
  // link out to https://www.openstreetmap.org/node/<id> for the
  // user to check / fix tags upstream.
  osmId?: string;
  // Marker that this came from the rich chargers archive — UI can
  // use it to decide whether to render the spec-list table.
  source: "chargers" | "basemap";
  // True when detection found DC connectors or maxPowerKW ≥ 50.
  isDCFC?: boolean;
  // NREL `facility_type` (HOTEL, MOTEL, INN, B_AND_B, RESORT,
  // PARKING_GARAGE, RESTAURANT, WORKPLACE, REST_STOP, ...) when
  // known. Empty / undefined for stations that didn't carry it in
  // the upstream catalog.
  facilityType?: string;
};

// HOTEL_FACILITY_TYPES drives the "hotels-with-L2" filter chip.
// All overnight-stay venue codes NREL AFDC ships.
export const HOTEL_FACILITY_TYPES: ReadonlySet<string> = new Set([
  "HOTEL",
  "MOTEL",
  "INN",
  "B_AND_B",
  "RESORT",
]);

type ArchiveCache = {
  url: string;
  pm: PMTiles;
};

// asyncPool runs `worker` over `items` with at most `limit` in-flight
// at a time. Drop-in replacement for Promise.all when the work fans
// out to a network resource — uncapped fan-out triggers Chrome's
// net::ERR_INSUFFICIENT_RESOURCES once the per-origin socket pool
// saturates (observed on chargers.pmtiles range reads for long
// corridors, hundreds of tiles × multiple PMTiles fetches each).
async function asyncPool<T>(
  limit: number,
  items: readonly T[],
  worker: (item: T, index: number) => Promise<void>,
): Promise<void> {
  let cursor = 0;
  async function run(): Promise<void> {
    while (true) {
      const i = cursor++;
      if (i >= items.length) return;
      await worker(items[i], i);
    }
  }
  const runners: Promise<void>[] = [];
  const n = Math.min(limit, items.length);
  for (let k = 0; k < n; k++) runners.push(run());
  await Promise.all(runners);
}

let chargersCache: ArchiveCache | null = null;
let basemapCache: ArchiveCache | null = null;

function getArchive(url: string, slot: "chargers" | "basemap"): PMTiles | null {
  if (!url) return null;
  const cache = slot === "chargers" ? chargersCache : basemapCache;
  if (cache && cache.url === url) return cache.pm;
  // Default fetch credentials are "same-origin", so the rivolt
  // session cookie rides along to /api/maps/tiles/* automatically.
  const pm = new PMTiles(new FetchSource(url));
  if (slot === "chargers") chargersCache = { url, pm };
  else basemapCache = { url, pm };
  return pm;
}

// Standard XYZ tile math.
function lonToTileX(lon: number, z: number): number {
  return Math.floor(((lon + 180) / 360) * (1 << z));
}
function latToTileY(lat: number, z: number): number {
  const rad = (lat * Math.PI) / 180;
  return Math.floor(
    ((1 - Math.log(Math.tan(rad) + 1 / Math.cos(rad)) / Math.PI) / 2) *
      (1 << z),
  );
}

function haversineMeters(
  la1: number,
  lo1: number,
  la2: number,
  lo2: number,
): number {
  const R = 6371000;
  const p1 = (la1 * Math.PI) / 180;
  const p2 = (la2 * Math.PI) / 180;
  const dp = ((la2 - la1) * Math.PI) / 180;
  const dl = ((lo2 - lo1) * Math.PI) / 180;
  const a =
    Math.sin(dp / 2) ** 2 +
    Math.cos(p1) * Math.cos(p2) * Math.sin(dl / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(a));
}

// Module-level memo keyed on (slot, rounded lat, rounded lon, kind, radius).
// 5 decimal places ~= 1.1m precision. Charge GPS is much noisier than that
// so rounding here is fine and avoids redundant tile fetches when the user
// flips between charge-detail tabs.
const memo = new Map<string, POI | null>();
function memoKey(
  slot: string,
  lat: number,
  lon: number,
  kind: string,
  radiusM: number,
): string {
  return `${slot}|${kind}|${lat.toFixed(5)}|${lon.toFixed(5)}|${radiusM}`;
}

// Tag readers. Tippecanoe preserves OSM tag values as strings;
// numeric tags occasionally come through typed when tippecanoe
// auto-detects, hence the dual handling.
function strProp(p: Record<string, unknown>, k: string): string | undefined {
  const v = p[k];
  return typeof v === "string" && v.length > 0 ? v : undefined;
}
function intProp(p: Record<string, unknown>, k: string): number | undefined {
  const v = p[k];
  if (typeof v === "number" && Number.isFinite(v)) return Math.round(v);
  if (typeof v === "string") {
    const n = parseInt(v.trim(), 10);
    return Number.isFinite(n) ? n : undefined;
  }
  return undefined;
}

// Parse OSM power-output strings like "150 kW", "22kW", "11000 W".
// Returns kW; undefined when the string isn't recognized.
function parsePowerKW(v: unknown): number | undefined {
  if (typeof v !== "string") return undefined;
  const s = v.trim().toLowerCase().replace(/,/g, "");
  if (!s) return undefined;
  let unit: "kw" | "w" = "kw";
  let num = s;
  if (s.endsWith("kw")) num = s.slice(0, -2).trim();
  else if (s.endsWith("w")) {
    unit = "w";
    num = s.slice(0, -1).trim();
  }
  const n = parseFloat(num);
  if (!Number.isFinite(n)) return undefined;
  return unit === "w" ? n / 1000 : n;
}

// extractSocketTypes returns the set of `socket:*` keys whose
// presence/value implies the connector type is supported. OSM
// convention uses keys like socket:type2_combo=4, socket:chademo=2.
// We ignore `socket:*:output` (those are power, not type) and
// values like "no" / "0" / "" which mean the connector isn't
// actually present.
function extractSocketTypes(
  props: Record<string, unknown>,
): string[] | undefined {
  const out: string[] = [];
  for (const [k, raw] of Object.entries(props)) {
    if (!k.startsWith("socket:")) continue;
    const rest = k.slice(7);
    if (rest.includes(":")) continue;
    if (!rest) continue;
    const v = typeof raw === "string" ? raw.trim().toLowerCase() : "";
    if (v === "" || v === "no" || v === "0") continue;
    out.push(rest);
  }
  if (out.length === 0) return undefined;
  out.sort();
  return out;
}

function extractMaxPowerKW(
  props: Record<string, unknown>,
): number | undefined {
  let best: number | undefined;
  const consider = (v: unknown) => {
    const kw = parsePowerKW(v);
    if (kw != null && (best == null || kw > best)) best = kw;
  };
  consider(props["charging:output"]);
  consider(props["maxpower"]);
  consider(props["maxpower:output"]);
  for (const [k, v] of Object.entries(props)) {
    if (k.startsWith("socket:") && k.endsWith(":output")) consider(v);
  }
  return best;
}

// extractOSMId reads the canonical OSM identifier when tippecanoe
// preserved it. Used by popups to link out to openstreetmap.org.
function extractOSMId(props: Record<string, unknown>): string | undefined {
  const at = strProp(props, "@id");
  if (at) return at;
  const id = props["id"];
  if (typeof id === "number") return `node/${id}`;
  if (typeof id === "string" && id.length > 0) return id;
  return undefined;
}

type LookupConfig = {
  // PMTiles archive zoom to query. The chargers archive is built at
  // z14; the basemap archive at z15. A 3x3 block at the archive's
  // max zoom comfortably covers radiusM <= 1km.
  z: number;
  // Vector-tile layer name to scan.
  layer: string;
  // Property bag → POI shape converter. Distinct paths for the rich
  // chargers archive vs the stripped basemap archive.
  toPOI: (
    p: Record<string, unknown>,
    base: Omit<POI, "source">,
  ) => POI;
};

const chargersLookup: LookupConfig = {
  z: 14,
  layer: "chargers",
  toPOI: (p, base) => ({
    ...base,
    source: "chargers",
    name: strProp(p, "name") ?? base.name,
    operator: strProp(p, "operator"),
    network: strProp(p, "network"),
    brand: strProp(p, "brand"),
    capacity: intProp(p, "capacity"),
    socketTypes: extractSocketTypes(p),
    maxPowerKW: extractMaxPowerKW(p),
    fee: strProp(p, "fee"),
    openingHours: strProp(p, "opening_hours"),
    osmId: extractOSMId(p),
    facilityType: strProp(p, "facility_type"),
  }),
};

const basemapLookup: LookupConfig = {
  z: 15,
  layer: "pois",
  toPOI: (p, base) => ({
    ...base,
    source: "basemap",
    name: strProp(p, "name") ?? base.name,
  }),
};

async function findInArchive(
  pm: PMTiles,
  cfg: LookupConfig,
  lat: number,
  lon: number,
  kinds: readonly string[] | null,
  radiusM: number,
): Promise<POI | null> {
  const z = cfg.z;
  const cx = lonToTileX(lon, z);
  const cy = latToTileY(lat, z);

  let best: POI | null = null;
  const offsets: [number, number][] = [];
  for (let dx = -1; dx <= 1; dx++) {
    for (let dy = -1; dy <= 1; dy++) offsets.push([dx, dy]);
  }
  await Promise.all(
    offsets.map(async ([dx, dy]) => {
      const tx = cx + dx;
      const ty = cy + dy;
      try {
        const result = await pm.getZxy(z, tx, ty);
        if (!result) return;
        const tile = new VectorTile(new Pbf(result.data as ArrayBuffer));
        const layer = tile.layers[cfg.layer];
        if (!layer) return;
        for (let i = 0; i < layer.length; i++) {
          const f = layer.feature(i);
          // Only filter by `kind` for the basemap archive — the
          // chargers archive is already pre-filtered to charging
          // stations at build time (osmium tags-filter), so every
          // feature in the layer is a charger.
          if (kinds) {
            const k = f.properties.kind;
            if (typeof k !== "string" || !kinds.includes(k)) continue;
          }
          const gj = f.toGeoJSON(tx, ty, z);
          if (gj.geometry.type !== "Point") continue;
          const [flon, flat] = gj.geometry.coordinates as [number, number];
          const dist = haversineMeters(lat, lon, flat, flon);
          if (dist > radiusM) continue;
          if (best && dist >= best.distanceM) continue;
          const props = f.properties as Record<string, unknown>;
          best = cfg.toPOI(props, {
            lat: flat,
            lon: flon,
            kind: "charging_station",
            distanceM: dist,
          });
        }
      } catch {
        // Missing tile (gap in the bbox extract), 4xx/5xx, or a
        // corrupted MVT payload — treat as no-data and let the
        // caller fall back to the next archive.
      }
    }),
  );
  return best;
}

// CORRIDOR_KM is the half-width of the charger search band around
// a planned route — 20 miles expressed in kilometres.
const CORRIDOR_KM = 32.2;

// projectedPath holds the per-segment projection of `path` into a
// local equirectangular metre grid PLUS the cumulative arc length
// at each vertex. Built once per route so per-candidate filtering
// is just a tight inner loop over precomputed numbers.
type ProjectedPath = {
  xs: number[];        // vertex x in metres
  ys: number[];        // vertex y in metres
  cumLen: number[];    // cumulative arc length at each vertex (metres)
  totalLen: number;    // total path length (metres)
  cosLat: number;      // projection's reference cosine (path centroid)
};

function projectPath(path: [number, number][]): ProjectedPath {
  // Use the path's centroid latitude as the projection reference.
  // For routes spanning multiple latitudes the per-test-point
  // cosLat used previously was correct only for that point;
  // sharing one reference here lets us reuse the same projection
  // for the cumulative-length math too.
  let sumLat = 0;
  for (const [la] of path) sumLat += la;
  const cosLat = Math.cos(((sumLat / path.length) * Math.PI) / 180);
  const xs: number[] = new Array(path.length);
  const ys: number[] = new Array(path.length);
  const cumLen: number[] = new Array(path.length);
  for (let i = 0; i < path.length; i++) {
    xs[i] = path[i][1] * 111320 * cosLat;
    ys[i] = path[i][0] * 110540;
  }
  cumLen[0] = 0;
  for (let i = 1; i < path.length; i++) {
    const dx = xs[i] - xs[i - 1];
    const dy = ys[i] - ys[i - 1];
    cumLen[i] = cumLen[i - 1] + Math.sqrt(dx * dx + dy * dy);
  }
  return { xs, ys, cumLen, totalLen: cumLen[cumLen.length - 1], cosLat };
}

// routeFilterMeters returns the minimum perpendicular distance in
// metres from (lat, lon) to `path` AFTER applying:
//   - "interior only" gating: reject points whose closest projection
//     is the very first or very last vertex of the route (clusters
//     radially around start/destination that aren't in the
//     direction of travel)
//   - "endpoint arc-length buffer": for routes >MIN_TRIM_LEN, reject
//     points that project to the first or last ENDPOINT_TRIM_M of
//     the route's arc length (a Supercharger 5 mi from the start
//     of a 500-mi trip isn't a road-trip stop)
//
// Returns Infinity when either gate rejects the point.
function routeFilterMeters(lat: number, lon: number, pp: ProjectedPath): number {
  if (pp.xs.length < 2) return Infinity;
  // Endpoint arc-length trim — only kick in once the route is long
  // enough to make "the first 20 mi" a meaningful filter rather
  // than "everything".
  const MIN_TRIM_LEN_M = 80_000; // 50 mi total route
  const ENDPOINT_TRIM_M = 32_000; // 20 mi at each end when active
  const applyTrim = pp.totalLen > MIN_TRIM_LEN_M;

  const x = lon * 111320 * pp.cosLat;
  const y = lat * 110540;
  const lastSeg = pp.xs.length - 2;
  let best = Infinity;
  let bestAlong = -1;        // arc-length position of best projection
  let bestIsRouteEndpoint = true;
  for (let i = 1; i < pp.xs.length; i++) {
    const ax = pp.xs[i - 1], ay = pp.ys[i - 1];
    const bx = pp.xs[i], by = pp.ys[i];
    const dx = bx - ax;
    const dy = by - ay;
    const segLen2 = dx * dx + dy * dy;
    const segLen = Math.sqrt(segLen2);
    const rawT = segLen2 > 0 ? ((x - ax) * dx + (y - ay) * dy) / segLen2 : 0;
    let t = rawT;
    let endpoint = false;
    if (t < 0) {
      t = 0;
      if (i - 1 === 0) endpoint = true;
    } else if (t > 1) {
      t = 1;
      if (i - 1 === lastSeg) endpoint = true;
    }
    const cx = ax + t * dx;
    const cy = ay + t * dy;
    const ddx = x - cx;
    const ddy = y - cy;
    const d2 = ddx * ddx + ddy * ddy;
    if (d2 < best) {
      best = d2;
      bestAlong = pp.cumLen[i - 1] + t * segLen;
      bestIsRouteEndpoint = endpoint;
    }
  }
  if (bestIsRouteEndpoint) return Infinity;
  if (applyTrim) {
    if (bestAlong < ENDPOINT_TRIM_M) return Infinity;
    if (pp.totalLen - bestAlong < ENDPOINT_TRIM_M) return Infinity;
  }
  return Math.sqrt(best);
}

// findChargersAlongPath returns all DCFC stations within CORRIDOR_KM
// of any point on the route. It expands the route's bounding box by
// CORRIDOR_KM on all sides, enumerates every z14 tile in that bbox,
// and fetches them in parallel. PMTiles' internal directory structure
// means most tile lookups are in-memory cache hits; only tiles that
// actually contain charger data trigger HTTP range reads.
//
// DCFC detection: accept a station if it has a known maxPowerKW ≥
// minPowerKW OR if it carries a DC connector tag (CCS1 / CHAdeMO /
// Tesla SC). The NREL AFDC source omits kW at the station level, so
// connector-type fallback is required for the primary archive.
export type ChargerFilter = "dcfc" | "l2" | "hotels" | "all";

export async function findChargersAlongPath(
  path: [number, number][],
  filter: ChargerFilter = "dcfc",
): Promise<POI[]> {
  if (path.length < 2) return [];
  await ensureConfig();
  const chargersURL = chargersPMTilesURL();
  if (!chargersURL) return [];
  const pm = getArchive(chargersURL, "chargers");
  if (!pm) return [];
  const z = chargersLookup.z;
  // Precompute the projected path + cumulative arc length so every
  // feature filter is a tight inner loop over numbers (no
  // per-candidate trig).
  const projected = projectPath(path);

  // Build the route bounding box.
  let minLat = path[0][0], maxLat = path[0][0];
  let minLon = path[0][1], maxLon = path[0][1];
  for (const [lat, lon] of path) {
    if (lat < minLat) minLat = lat;
    if (lat > maxLat) maxLat = lat;
    if (lon < minLon) minLon = lon;
    if (lon > maxLon) maxLon = lon;
  }

  // Expand by CORRIDOR_KM degrees on all sides.
  const midLat = (minLat + maxLat) / 2;
  const dLat = CORRIDOR_KM / 111.32;
  const dLon = CORRIDOR_KM / (111.32 * Math.cos((midLat * Math.PI) / 180));
  minLat -= dLat; maxLat += dLat;
  minLon -= dLon; maxLon += dLon;

  // Enumerate all z14 tiles within the expanded bbox.
  // TMS convention: y increases downward — northernmost lat → smallest y.
  const txMin = lonToTileX(minLon, z);
  const txMax = lonToTileX(maxLon, z);
  const tyMin = latToTileY(maxLat, z);
  const tyMax = latToTileY(minLat, z);

  const tiles: [number, number][] = [];
  for (let tx = txMin; tx <= txMax; tx++) {
    for (let ty = tyMin; ty <= tyMax; ty++) {
      tiles.push([tx, ty]);
    }
  }

  const seen = new Map<string, POI>();
  // 8 concurrent tile fetches matches Chrome's per-origin HTTP/1.1
  // socket limit; HTTP/2 sites can go higher but 8 is a safe ceiling
  // that leaves room for the basemap layer + app API on the same
  // origin.
  await asyncPool(8, tiles, async ([tx, ty]) => {
      try {
        const result = await pm.getZxy(z, tx, ty);
        if (!result) return;
        const tile = new VectorTile(new Pbf(result.data as ArrayBuffer));
        const layer = tile.layers[chargersLookup.layer];
        if (!layer) return;
        for (let i = 0; i < layer.length; i++) {
          const f = layer.feature(i);
          const gj = f.toGeoJSON(tx, ty, z);
          if (gj.geometry.type !== "Point") continue;
          const [flon, flat] = gj.geometry.coordinates as [number, number];
          const props = f.properties as Record<string, unknown>;
          const maxKW = extractMaxPowerKW(props);
          // NREL's TESLA connector covers both Supercharger (DCFC) and
          // Destination (L2), so tesla_supercharger alone is NOT a
          // reliable DC indicator. Use explicit DC count when the
          // pipeline emits it; fall back to unambiguous DC connectors
          // (CCS1, CHAdeMO) or known maxPowerKW ≥ 50.
          const dcfcCount = typeof props["dcfc_count"] === "number" ? (props["dcfc_count"] as number) : undefined;
          const l2Count = typeof props["l2_count"] === "number" ? (props["l2_count"] as number) : undefined;
          const hasUnambiguousDC =
            props["socket:type1_combo"] === "yes" ||
            props["socket:chademo"] === "yes";
          let isDCFC: boolean;
          if (dcfcCount !== undefined) {
            isDCFC = dcfcCount > 0;
          } else {
            isDCFC = hasUnambiguousDC || (maxKW !== undefined && maxKW >= 50);
          }
          // For L2: prefer explicit l2_count when present, else infer
          // from absence of DCFC signal.
          const isL2 = l2Count !== undefined ? l2Count > 0 : !isDCFC;
          if (filter === "dcfc" && !isDCFC) continue;
          if (filter === "l2" && !isL2) continue;
          if (filter === "hotels") {
            // Hotels-with-L2: must carry an overnight-venue
            // facility_type AND have at least one L2 stall.
            // Strict on facility_type (no keyword fallback in
            // basic version) so we don't false-positive on a
            // "Hilton Garden" road sign at a random parking lot.
            const ft = (typeof props["facility_type"] === "string"
              ? (props["facility_type"] as string)
              : "").toUpperCase();
            if (!HOTEL_FACILITY_TYPES.has(ft)) continue;
            if (!isL2) continue;
          }
          // Filter by perpendicular distance to the route line PLUS
          // arc-length distance from the endpoints. The bbox-only
          // filter dragged in everything within the rectangle, and a
          // pure perpendicular filter still kept the metro cluster
          // around the destination (chargers project onto the last
          // few segments at < 20 mi perp distance). routeFilterMeters
          // rejects (a) closest-projection-is-an-endpoint and (b)
          // closest-projection-within-20mi-arc-of-an-endpoint for
          // long routes.
          if (routeFilterMeters(flat, flon, projected) > CORRIDOR_KM * 1000) continue;
          const k = `${flat.toFixed(5)},${flon.toFixed(5)}`;
          if (!seen.has(k)) {
            seen.set(k, { ...chargersLookup.toPOI(props, {
              lat: flat,
              lon: flon,
              kind: "charging_station",
              distanceM: 0,
            }), isDCFC });
          }
        }
      } catch {
        // Missing tile or corrupted MVT — skip
      }
    },
  );
  return Array.from(seen.values());
}

// findNearestCharger queries the chargers archive first; on miss
// (no archive configured, or no feature within radius), falls back
// to the basemap pois layer. 250m default radius matches typical
// "I parked near this thing" GPS noise — within a parking lot but
// not across a freeway.
export async function findNearestCharger(
  lat: number,
  lon: number,
  radiusM = 250,
): Promise<POI | null> {
  // Wait for /api/config before reading the archive URLs — synchronous
  // callers (e.g. ChargeMap's effect on mount) would otherwise see ""
  // and silently fall through to the metadata-stripped basemap layer.
  await ensureConfig();
  // Prefer the rich chargers archive.
  const chargersURL = chargersPMTilesURL();
  const chargersKey = memoKey("chargers", lat, lon, "charging_station", radiusM);
  if (chargersURL) {
    let hit: POI | null | undefined;
    if (memo.has(chargersKey)) {
      hit = memo.get(chargersKey) ?? null;
    } else {
      const pm = getArchive(chargersURL, "chargers");
      if (pm) {
        hit = await findInArchive(
          pm,
          chargersLookup,
          lat,
          lon,
          null,
          radiusM,
        );
        memo.set(chargersKey, hit);
      }
    }
    if (hit) return hit;
  }

  // Basemap fallback: low-fidelity but better than recorded GPS.
  const basemapURL = tilesPMTilesURL();
  if (!basemapURL) return null;
  const basemapKey = memoKey("basemap", lat, lon, "charging_station", radiusM);
  if (memo.has(basemapKey)) return memo.get(basemapKey) ?? null;
  const pm = getArchive(basemapURL, "basemap");
  if (!pm) return null;
  const hit = await findInArchive(
    pm,
    basemapLookup,
    lat,
    lon,
    ["charging_station"],
    radiusM,
  );
  memo.set(basemapKey, hit);
  return hit;
}
