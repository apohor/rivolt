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
import { tilesPMTilesURL, chargersPMTilesURL } from "./config";

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
};

type ArchiveCache = {
  url: string;
  pm: PMTiles;
};

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

// findChargersAlongPath scans the chargers archive for DCFC stations
// (≥ minPowerKW) within the corridor of a route polyline. It samples
// the path at ~10 km intervals, collects the 3×3 z14 tile block
// around each sample, deduplicates tiles across samples, and fetches
// them all in parallel. Returns [] when no chargers archive is
// configured (i.e. chargersPMTilesURL() is empty).
export async function findChargersAlongPath(
  path: [number, number][],
  minPowerKW = 50,
): Promise<POI[]> {
  if (path.length < 2) return [];
  const chargersURL = chargersPMTilesURL();
  if (!chargersURL) return [];
  const pm = getArchive(chargersURL, "chargers");
  if (!pm) return [];
  const z = chargersLookup.z;

  const tileSet = new Set<string>();
  const tilesToFetch: [number, number][] = [];
  const addPoint = (lat: number, lon: number) => {
    const cx = lonToTileX(lon, z);
    const cy = latToTileY(lat, z);
    for (let dx = -1; dx <= 1; dx++) {
      for (let dy = -1; dy <= 1; dy++) {
        const k = `${cx + dx},${cy + dy}`;
        if (!tileSet.has(k)) {
          tileSet.add(k);
          tilesToFetch.push([cx + dx, cy + dy]);
        }
      }
    }
  };

  const SAMPLE_M = 10_000;
  let accumulated = 0;
  addPoint(path[0][0], path[0][1]);
  for (let i = 1; i < path.length; i++) {
    const dist = haversineMeters(
      path[i - 1][0], path[i - 1][1],
      path[i][0], path[i][1],
    );
    accumulated += dist;
    if (accumulated >= SAMPLE_M) {
      addPoint(path[i][0], path[i][1]);
      accumulated = 0;
    }
  }
  addPoint(path[path.length - 1][0], path[path.length - 1][1]);

  const seen = new Map<string, POI>();
  await Promise.all(
    tilesToFetch.map(async ([tx, ty]) => {
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
          // NREL AFDC data (our primary archive) carries no kW field —
          // fall back to connector-type detection. Presence of a DC
          // connector (CCS1 / CHAdeMO / Tesla SC) is a reliable proxy
          // for "fast charger"; Level-1/2 J1772 and NEMA outlets
          // are excluded by this check.
          const hasDCConnector =
            props["socket:type1_combo"] === "yes" ||
            props["socket:chademo"] === "yes" ||
            props["socket:tesla_supercharger"] === "yes";
          if (!hasDCConnector && (maxKW === undefined || maxKW < minPowerKW)) continue;
          const k = `${flat.toFixed(5)},${flon.toFixed(5)}`;
          if (!seen.has(k)) {
            seen.set(k, chargersLookup.toPOI(props, {
              lat: flat,
              lon: flon,
              kind: "charging_station",
              distanceM: 0,
            }));
          }
        }
      } catch {
        // Missing tile or corrupted MVT — skip
      }
    }),
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
