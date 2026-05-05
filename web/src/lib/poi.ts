// POI lookup against the self-hosted PMTiles vector basemap.
//
// Why query the basemap pmtiles directly instead of e.g. Overpass API
// or a server-side endpoint:
//   - Same origin already (the rivolt API proxies the .pmtiles file
//     at /api/maps/tiles/* with byte-range support), so authn is
//     automatic via the session cookie.
//   - PMTiles + @mapbox/vector-tile are already in the SPA bundle as
//     transitive deps of protomaps-leaflet; using them directly costs
//     nothing extra at runtime.
//   - No backend dependency on Go pmtiles libs or Overpass uptime.
//
// What we actually get from the protomaps planet build:
//   - The `pois` data layer is populated up to the archive's max zoom
//     (z=15 for the planet build; charging stations have an OSM-tag
//     min_zoom hint of 16 but the data points are placed in z15 tiles
//     and rendered with overzoom). A single z15 tile covers ~1.2 km
//     across in TX latitudes, so a single tile lookup is enough for
//     any reasonable "nearest charger" radius.
//   - Tags are heavily stripped in the planet build: only `name`,
//     `kind`, and `min_zoom` survive on charging_station POIs.
//     Operator/network/socket info is NOT available; if we want it
//     later we'd build our own POI tiles from raw OSM.

import Pbf from "pbf";
import { VectorTile } from "@mapbox/vector-tile";
import { PMTiles, FetchSource } from "pmtiles";
import { tilesPMTilesURL } from "./config";

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
};

let pmCache: PMTiles | null = null;
let pmCacheURL = "";

function getPM(): PMTiles | null {
  const url = tilesPMTilesURL();
  if (!url) return null;
  // Reset the cached archive if config flipped to a different URL
  // (won't happen in practice today but cheap to handle).
  if (pmCache && pmCacheURL === url) return pmCache;
  // Default fetch credentials are "same-origin", so the rivolt API
  // session cookie rides along to /api/maps/tiles/* automatically.
  // No custom Headers needed.
  pmCache = new PMTiles(new FetchSource(url));
  pmCacheURL = url;
  return pmCache;
}

// Standard XYZ tile math. Floors so the returned (x,y) is the tile
// that contains the given lat/lon at zoom z.
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

// Haversine distance in meters. Cheap enough to call inside the
// per-feature loop; we only iterate features in a single tile.
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

// Module-level memo keyed on (rounded lat,lon,kind,radius). Charge
// detail pages remount on tab switch; without a cache that triggers
// repeated tile fetches for the same query.
const memo = new Map<string, POI | null>();
function memoKey(
  lat: number,
  lon: number,
  kind: string,
  radiusM: number,
): string {
  // 5 decimal places ~= 1.1m precision. Rounding here is fine since
  // recorded charge GPS is much noisier than that.
  return `${kind}|${lat.toFixed(5)}|${lon.toFixed(5)}|${radiusM}`;
}

// findNearestPOI scans a 3x3 block of z15 data tiles centered on the
// query point and returns the closest feature whose `kind` is in the
// allow-list, or null when nothing is within radiusM. The 3x3 block
// is overkill for radiusM <= 250 but tile reads are byte-range +
// HTTP-cached, so it's effectively free after the first hit.
export async function findNearestPOI(
  lat: number,
  lon: number,
  kinds: readonly string[],
  radiusM = 250,
): Promise<POI | null> {
  const pm = getPM();
  if (!pm) return null;

  const key = memoKey(lat, lon, kinds.join(","), radiusM);
  if (memo.has(key)) return memo.get(key) ?? null;

  // Use the archive's actual max zoom — z=15 for the protomaps
  // planet build. If we ever swap to a custom build with z=16,
  // bumping this constant is the only required change.
  const z = 15;
  const cx = lonToTileX(lon, z);
  const cy = latToTileY(lat, z);

  let best: POI | null = null;
  // 3x3 block. Each tile fetch is independent so we kick them off
  // in parallel; the FetchSource will share connections.
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
        const layer = tile.layers["pois"];
        if (!layer) return;
        for (let i = 0; i < layer.length; i++) {
          const f = layer.feature(i);
          const k = f.properties.kind;
          if (typeof k !== "string" || !kinds.includes(k)) continue;
          // toGeoJSON projects MVT-local coords back to lng/lat.
          const gj = f.toGeoJSON(tx, ty, z);
          if (gj.geometry.type !== "Point") continue;
          const [flon, flat] = gj.geometry.coordinates as [number, number];
          const dist = haversineMeters(lat, lon, flat, flon);
          if (dist > radiusM) continue;
          if (!best || dist < best.distanceM) {
            const nameProp = f.properties.name;
            best = {
              lat: flat,
              lon: flon,
              name: typeof nameProp === "string" ? nameProp : undefined,
              kind: k,
              distanceM: dist,
            };
          }
        }
      } catch {
        // Missing tile (gap in the bbox extract), 4xx/5xx, or a
        // corrupted MVT payload — treat as no-data and let the
        // caller fall back to the recorded GPS dot.
      }
    }),
  );

  memo.set(key, best);
  return best;
}

// findNearestCharger is a thin wrapper for the most common case.
// 250m default radius matches typical "I parked near this thing"
// GPS noise on a phone — within a parking lot but not across a
// freeway.
export async function findNearestCharger(
  lat: number,
  lon: number,
  radiusM = 250,
): Promise<POI | null> {
  return findNearestPOI(lat, lon, ["charging_station"], radiusM);
}
