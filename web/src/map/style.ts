// The map style, built in one pure function from the server's advertised same-origin URLs: the quiet Protomaps basemap restyled to the app's
// tokens (dark and light), with the hillshade when a terrain route is advertised. It makes no choice about the network: every URL in it is
// same-origin or a pmtiles:// URL on the same origin, there is no sprite and no layer asks for an icon. style.test.ts audits exactly that.
//
// Ported from ux's maps prototype (_shared/maps-mockups); ux owns the colours.
import { layers, namedFlavor, type Flavor as PMFlavor } from "@protomaps/basemaps";
import type { LayerSpecification, StyleSpecification } from "maplibre-gl";

export type MapFlavor = "dark" | "light";

export type StyleInput = {
  flavor: MapFlavor;
  // The page's origin ("https://rivolt.dev"); every URL in the style is absolute on it.
  origin: string;
  // Same-origin path of the .pmtiles archive ("/api/maps/tiles/us.pmtiles"); required.
  tilesPath: string;
  // Same-origin glyph template ("/api/maps/glyphs/{fontstack}/{range}.pbf"). Empty (a server that does not advertise it): no label layers,
  // never a fetch to a font host.
  glyphsPath: string;
  // Terrain: the template and zooms the server advertises; null/undefined draws no hillshade.
  dem?: { url: string; minZoom: number; maxZoom: number } | null;
};

// The terrain dataset requires its sources to be credited whenever the hillshade is on.
export const TERRAIN_ATTRIBUTION =
  "Terrain: USGS 3DEP, GMTED2010 and SRTM; Natural Resources Canada (Open Government Licence – Canada); INEGI; ArcticDEM; NOAA ETOPO1";

export const BASEMAP_ATTRIBUTION =
  '<a href="https://protomaps.com" target="_blank" rel="noopener">Protomaps</a> © <a href="https://openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a>';

const FLAVORS: Record<MapFlavor, Partial<PMFlavor>> = {
  dark: {
    background: "#080B10", earth: "#0B0F15", water: "#0A1822", park_a: "#0C1612", park_b: "#0C1612", wood_a: "#0C1612", wood_b: "#0C1612", scrub_a: "#0D1511", scrub_b: "#0D1511",
    glacier: "#10151C", sand: "#0F1217", beach: "#0F1217", hospital: "#0B0F15", industrial: "#0B0F15", school: "#0B0F15", zoo: "#0B0F15", military: "#0B0F15", pedestrian: "#0B0F15",
    aerodrome: "#0B0F15", runway: "#161B22", buildings: "#0B0F15",
    minor_service: "#141920", minor_a: "#171C23", minor_b: "#171C23", link: "#1B212A", other: "#141920", major: "#202733", highway: "#2A3340", railway: "#161B22", boundaries: "#232A33",
    minor_casing: "#0B0F15", minor_service_casing: "#0B0F15", link_casing: "#0B0F15", major_casing_early: "#0B0F15", major_casing_late: "#0B0F15", highway_casing_early: "#0B0F15", highway_casing_late: "#0B0F15",
    city_label: "#9AA3B0", city_label_halo: "#080B10", subplace_label: "#6F7885", subplace_label_halo: "#080B10", state_label: "#4A525E", state_label_halo: "#080B10", country_label: "#4A525E",
    ocean_label: "#3C5262", roads_label_major: "#6F7885", roads_label_major_halo: "#080B10", roads_label_minor: "#586070", roads_label_minor_halo: "#080B10", address_label: "#586070", address_label_halo: "#080B10",
  },
  light: {
    background: "#EEF0F4", earth: "#EDEFF3", water: "#D6E3EE", park_a: "#E4EBE6", park_b: "#E4EBE6", wood_a: "#E1E9E3", wood_b: "#E1E9E3", scrub_a: "#E6EBE6", scrub_b: "#E6EBE6",
    glacier: "#F2F4F7", sand: "#EFEFEA", beach: "#EFEFEA", hospital: "#EDEFF3", industrial: "#EBEDF1", school: "#EDEFF3", zoo: "#EDEFF3", military: "#EDEFF3", pedestrian: "#F3F4F7",
    aerodrome: "#EDEFF3", runway: "#E2E5EA", buildings: "#EDEFF3",
    minor_service: "#F7F8FA", minor_a: "#F7F8FA", minor_b: "#F7F8FA", link: "#F7F8FA", other: "#F7F8FA", major: "#FAFBFC", highway: "#FFFFFF", railway: "#DADDE3", boundaries: "#C9CED6",
    minor_casing: "#E4E7EC", minor_service_casing: "#E4E7EC", link_casing: "#E1E4EA", major_casing_early: "#DDE1E7", major_casing_late: "#DDE1E7", highway_casing_early: "#D2D7DF", highway_casing_late: "#D2D7DF",
    city_label: "#4B5563", city_label_halo: "#EEF0F4", subplace_label: "#7A838F", subplace_label_halo: "#EEF0F4", state_label: "#A4ABB5", state_label_halo: "#EEF0F4", country_label: "#A4ABB5",
    ocean_label: "#8AA2B6", roads_label_major: "#6B7480", roads_label_major_halo: "#FFFFFF", roads_label_minor: "#8C95A1", roads_label_minor_halo: "#FFFFFF", address_label: "#8C95A1", address_label_halo: "#FFFFFF",
  },
};

// The named flavour hard-codes its landcover and park greens; restyle them to a hint of tint over the earth colour.
const LANDCOVER = {
  dark: { grassland: "#0C1411", barren: "#0D1015", urban_area: "#0D1117", farmland: "#0C1310", glacier: "#10151C", scrub: "#0C1310", other: "#0B1310", park: "#0C1612" },
  light: { grassland: "#E6ECE8", barren: "#EEEEEA", urban_area: "#E9EBEF", farmland: "#E7ECE8", glacier: "#F4F6F9", scrub: "#E9EDE9", other: "#E4EBE7", park: "#E1E9E4" },
} as const;

// A quiet basemap: no POIs, buildings, addresses, minor road names, shields, waterway names or one-way arrows.
const DROP = /^(pois|buildings|address_label|roads_labels_minor|roads_shields|roads_oneway|water_waterway_label|earth_label_islands|places_subplace|landuse_(zoo|school|hospital|industrial|beach|aerodrome|runway|pedestrian|pier)|roads_(runway|taxiway|pier)|water_stream)/;

const HILLSHADE = {
  dark: { "hillshade-exaggeration": 0.18, "hillshade-shadow-color": "#000000", "hillshade-highlight-color": "#3A4656", "hillshade-accent-color": "#10161E", "hillshade-illumination-direction": 315 },
  light: { "hillshade-exaggeration": 0.16, "hillshade-shadow-color": "#6C7684", "hillshade-highlight-color": "#FFFFFF", "hillshade-accent-color": "#B9C0CB", "hillshade-illumination-direction": 315 },
} as const;

// The route's own colours per flavour are not part of the style; route.ts draws them as layers on top.

export function buildStyle(input: StyleInput): StyleSpecification {
  const { flavor, origin, tilesPath, glyphsPath, dem } = input;
  const f: PMFlavor = { ...namedFlavor(flavor), ...FLAVORS[flavor] } as PMFlavor;
  const LC = LANDCOVER[flavor];
  const base = (layers("protomaps", f, { lang: "en" }) as LayerSpecification[])
    .filter((l) => !DROP.test(l.id) && (glyphsPath !== "" || l.type !== "symbol"))
    .map((l): LayerSpecification => {
      // Towns only: the larger ones (population_rank 9 and up); the rest is clutter under a route.
      if (l.id === "places_locality") l = { ...l, filter: ["all", ["==", ["get", "kind"], "locality"], [">=", ["to-number", ["get", "population_rank"], 0], 9]] } as LayerSpecification;
      if (l.id === "landcover") {
        l = { ...l, paint: { ...(l as { paint?: object }).paint, "fill-color": ["match", ["get", "kind"], "grassland", LC.grassland, "barren", LC.barren, "urban_area", LC.urban_area, "farmland", LC.farmland, "glacier", LC.glacier, "scrub", LC.scrub, LC.other] } } as LayerSpecification;
      }
      if (l.id === "landuse_park" || l.id === "landuse_urban_green") {
        l = { ...l, paint: { ...(l as { paint?: object }).paint, "fill-color": LC.park, "fill-opacity": 0.9 } } as LayerSpecification;
      }
      const layout = (l as { layout?: Record<string, unknown> }).layout;
      if (!layout) return l;
      const clean = { ...layout };
      for (const k of Object.keys(clean)) if (k.startsWith("icon-")) delete clean[k];
      return { ...l, layout: clean } as LayerSpecification;
    });

  const sources: StyleSpecification["sources"] = {
    protomaps: { type: "vector", url: `pmtiles://${origin}${tilesPath}`, attribution: BASEMAP_ATTRIBUTION },
  };
  let out: LayerSpecification[] = base;
  if (dem?.url) {
    sources.dem = {
      type: "raster-dem",
      tiles: [origin + dem.url],
      encoding: "terrarium",
      tileSize: 256,
      minzoom: dem.minZoom,
      maxzoom: dem.maxZoom,
      attribution: TERRAIN_ATTRIBUTION,
    };
    // The hillshade goes under water, roads and labels, above the land.
    const at = base.findIndex((l) => l.id === "water");
    const i = at < 0 ? Math.min(1, base.length) : at;
    out = [...base.slice(0, i), { id: "hillshade", type: "hillshade", source: "dem", paint: HILLSHADE[flavor] } as LayerSpecification, ...base.slice(i)];
  }
  return { version: 8, ...(glyphsPath ? { glyphs: origin + glyphsPath } : {}), sources, layers: out };
}

// plainStyle is the map with no basemap at all (the server advertises no tile archive): a flat background in the flavour's colour. The route
// still draws on it, with a notice.
export function plainStyle(flavor: MapFlavor): StyleSpecification {
  const bg = FLAVORS[flavor].background ?? "#0a0a0a";
  return { version: 8, sources: {}, layers: [{ id: "background", type: "background", paint: { "background-color": bg } }] };
}

// styleURLs lists every URL a style would have the browser fetch (tile, glyph, sprite and archive URLs), for the same-origin audit.
export function styleURLs(style: StyleSpecification): string[] {
  const urls: string[] = [];
  if (style.glyphs) urls.push(style.glyphs);
  if (style.sprite) urls.push(...(Array.isArray(style.sprite) ? style.sprite.map((s) => s.url) : [style.sprite]));
  for (const s of Object.values(style.sources)) {
    const src = s as { url?: string; tiles?: string[] };
    if (src.url) urls.push(src.url);
    if (src.tiles) urls.push(...src.tiles);
  }
  return urls;
}
